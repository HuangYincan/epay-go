package observerstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"

	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var ErrGrant = errors.New("invalid_grant")
var ErrEnrollment = errors.New("enrollment_conflict")
var ErrBinding = errors.New("binding_changed")
var ErrRate = errors.New("rate_limited")

const credentialLifetime int64 = 30 * 24 * 60 * 60 * 1000

// PairingService has a single startup-fixed trust domain. No caller-supplied
// environment, origin, or source class can expand it.
type PairingService struct {
	db                               *gorm.DB
	environment, sourceClass, origin string
	cipher                           *KeyCipher
}

func NewPairingService(db *gorm.DB, environment, sourceClass, origin string, cipher *KeyCipher) (*PairingService, error) {
	if db == nil || cipher == nil || !observer.ValidIdentifier(environment) || !observer.ValidOrigin(origin) || (sourceClass != "synthetic" && sourceClass != "notification") {
		return nil, ErrInput
	}
	return &PairingService{db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}), environment, sourceClass, origin, cipher}, nil
}
func (p *PairingService) Origin() string { return p.origin }

type grantRow struct {
	GrantID, Environment, ReceiverID, Provider, SourceClass, Origin, Mode, TargetDeviceID string
	ExpectedBindingVersion                                                                int
	ExpiresAtMs                                                                           int64
	Revoked                                                                               bool
	EnrollmentID, KeyID                                                                   string
	ResumeHash                                                                            []byte
}
type credentialRow struct {
	CredentialScope
	EncryptionKeyID          string
	KeyCiphertext            []byte
	State                    string
	NotBeforeMs, ExpiresAtMs int64
}

func (p *PairingService) scope(g grantRow) observer.PairingScope {
	return observer.PairingScope{SchemaVersion: 1, GrantID: g.GrantID, Origin: g.Origin, Environment: g.Environment, ReceiverID: g.ReceiverID, Provider: g.Provider, SourceClass: g.SourceClass, Mode: g.Mode, ExpectedBindingVersion: g.ExpectedBindingVersion, DeviceID: g.TargetDeviceID, ExpiresAtMs: g.ExpiresAtMs}
}
func validPairingTime(now int64) bool { return now >= 0 && now <= observer.MaxTime-credentialLifetime }
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrStorage
	}
	return hex.EncodeToString(b), nil
}
func randomSecret() (string, error) {
	b := make([]byte, 32)
	defer clear(b)
	if _, err := rand.Read(b); err != nil {
		return "", ErrStorage
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func digestSecret(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
func publicError(err error) error {
	for _, known := range []error{ErrInput, ErrScope, ErrGrant, ErrEnrollment, ErrBinding, ErrRate} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrStorage
}
func (p *PairingService) transaction(ctx context.Context, action func(*gorm.DB) error) error {
	err := p.db.WithContext(ctx).Transaction(action, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return publicError(err)
	}
	return nil
}

// All authority mutations lock environment -> receiver -> grant -> credentials.
func (p *PairingService) lockReceiver(tx *gorm.DB, receiver string) (string, error) {
	var environment struct {
		Enabled     bool
		SourceClass string
	}
	result := tx.Raw(`SELECT enabled,source_class FROM observer_environments WHERE environment=? FOR SHARE`, p.environment).Scan(&environment)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 || !environment.Enabled || environment.SourceClass != p.sourceClass {
		return "", ErrScope
	}
	var row struct {
		Enabled  bool
		Provider string
	}
	result = tx.Raw(`SELECT enabled,provider FROM observer_receivers WHERE environment=? AND receiver_id=? FOR UPDATE`, p.environment, receiver).Scan(&row)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 || !row.Enabled {
		return "", ErrScope
	}
	return row.Provider, nil
}
func currentBinding(tx *gorm.DB, environment, receiver string) (version int, device string, active bool, err error) {
	var row struct {
		BindingVersion int
		DeviceID       string
		Active         bool
	}
	err = tx.Raw(`SELECT binding_version,device_id,active FROM observer_bindings WHERE environment=? AND receiver_id=? ORDER BY binding_version DESC LIMIT 1`, environment, receiver).Scan(&row).Error
	return row.BindingVersion, row.DeviceID, row.Active, err
}

// CreateGrant is only called behind administrator authorization. Actor is checked
// again in the DB; the token is returned once and only its digest is stored.
func (p *PairingService) CreateGrant(ctx context.Context, receiver, mode string, actor, now int64) (observer.PairingScope, string, error) {
	if !observer.ValidIdentifier(receiver) || (mode != "replace" && mode != "rotate") || actor <= 0 || !validPairingTime(now) {
		return observer.PairingScope{}, "", ErrInput
	}
	token, err := randomSecret()
	if err != nil {
		return observer.PairingScope{}, "", err
	}
	id, err := randomID()
	if err != nil {
		return observer.PairingScope{}, "", err
	}
	var result observer.PairingScope
	err = p.transaction(ctx, func(tx *gorm.DB) error {
		var admins int64
		if err := tx.Raw(`SELECT count(*) FROM admins WHERE id=? AND deleted_at IS NULL`, actor).Scan(&admins).Error; err != nil {
			return err
		}
		if admins != 1 {
			return ErrScope
		}
		provider, err := p.lockReceiver(tx, receiver)
		if err != nil {
			return err
		}
		generation, device, active, err := currentBinding(tx, p.environment, receiver)
		if err != nil {
			return err
		}
		if generation == 2147483647 && mode == "replace" || mode == "rotate" && !active {
			return ErrBinding
		}
		if mode == "replace" {
			device = ""
		}
		g := grantRow{GrantID: id, Environment: p.environment, ReceiverID: receiver, Provider: provider, SourceClass: p.sourceClass, Origin: p.origin, Mode: mode, TargetDeviceID: device, ExpectedBindingVersion: generation, ExpiresAtMs: now + 300000}
		err = tx.Exec(`INSERT INTO observer_pairing_grants(grant_id,token_hash,environment,receiver_id,provider,source_class,origin,mode,expected_binding_version,target_device_id,created_by,created_at_ms,expires_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, digestSecret(token), p.environment, receiver, provider, p.sourceClass, p.origin, mode, generation, device, actor, now, g.ExpiresAtMs).Error
		result = p.scope(g)
		return err
	})
	if err != nil {
		return observer.PairingScope{}, "", err
	}
	return result, token, nil
}

// Persistent budget is committed even when the later resume secret is incorrect.
// A backwards clock does not reset the window or replenish the budget.
func (p *PairingService) charge(ctx context.Context, token string, now int64) (grantRow, error) {
	if !observer.ValidSecret(token) || !validPairingTime(now) {
		return grantRow{}, ErrGrant
	}
	var g grantRow
	result := p.db.WithContext(ctx).Raw(`SELECT * FROM observer_pairing_grants WHERE token_hash=? AND environment=? AND origin=?`, digestSecret(token), p.environment, p.origin).Scan(&g)
	if result.Error != nil {
		return g, ErrStorage
	}
	if result.RowsAffected != 1 || g.Revoked || now >= g.ExpiresAtMs {
		return g, ErrGrant
	}
	var attempts int
	result = p.db.WithContext(ctx).Raw(`INSERT INTO observer_pairing_rate(token_hash,window_start_ms,attempts) VALUES (@token_hash,@now,1)
 ON CONFLICT(token_hash) DO UPDATE SET
 attempts=CASE WHEN excluded.window_start_ms >= observer_pairing_rate.window_start_ms+60000 THEN 1 ELSE LEAST(observer_pairing_rate.attempts+1,21) END,
 window_start_ms=CASE WHEN excluded.window_start_ms >= observer_pairing_rate.window_start_ms+60000 THEN excluded.window_start_ms ELSE observer_pairing_rate.window_start_ms END RETURNING attempts`, sql.Named("token_hash", digestSecret(token)), sql.Named("now", now)).Scan(&attempts)
	if result.Error != nil {
		return g, ErrStorage
	}
	if attempts > 20 {
		return g, ErrRate
	}
	return g, nil
}
func (p *PairingService) lockGrant(tx *gorm.DB, token string, hint grantRow, now int64) (grantRow, error) {
	provider, err := p.lockReceiver(tx, hint.ReceiverID)
	if err != nil {
		return grantRow{}, err
	}
	var g grantRow
	result := tx.Raw(`SELECT * FROM observer_pairing_grants WHERE token_hash=? FOR UPDATE`, digestSecret(token)).Scan(&g)
	if result.Error != nil {
		return g, result.Error
	}
	if result.RowsAffected != 1 || g.Revoked || now >= g.ExpiresAtMs {
		return g, ErrGrant
	}
	if g.Environment != p.environment || g.SourceClass != p.sourceClass || g.Origin != p.origin || g.Provider != provider || g.ReceiverID != hint.ReceiverID {
		return g, ErrScope
	}
	return g, nil
}
func (p *PairingService) credential(tx *gorm.DB, g grantRow, now int64) (credentialRow, error) {
	var c credentialRow
	result := tx.Raw(`SELECT c.* FROM observer_credentials c JOIN observer_bindings b USING(environment,receiver_id,binding_version,device_id,provider) JOIN observer_devices d USING(environment,device_id) WHERE c.key_id=? AND b.active AND d.enabled FOR UPDATE OF c`, g.KeyID).Scan(&c)
	if result.Error != nil {
		return c, result.Error
	}
	if result.RowsAffected != 1 || c.State == "revoked" || c.NotBeforeMs > now || now >= c.ExpiresAtMs {
		return c, ErrScope
	}
	return c, nil
}
func (p *PairingService) Inspect(ctx context.Context, token string, now int64) (observer.PairingScope, error) {
	hint, err := p.charge(ctx, token, now)
	if err != nil {
		return observer.PairingScope{}, err
	}
	var result observer.PairingScope
	err = p.transaction(ctx, func(tx *gorm.DB) error {
		g, err := p.lockGrant(tx, token, hint, now)
		if err != nil {
			return err
		}
		if g.KeyID != "" {
			if _, err := p.credential(tx, g, now); err != nil {
				return err
			}
		} else {
			version, device, active, err := currentBinding(tx, p.environment, g.ReceiverID)
			if err != nil {
				return err
			}
			if version != g.ExpectedBindingVersion || g.Mode == "rotate" && (!active || device != g.TargetDeviceID) {
				return ErrBinding
			}
		}
		result = p.scope(g)
		return nil
	})
	if err != nil {
		return observer.PairingScope{}, err
	}
	return result, nil
}
func (p *PairingService) Redeem(ctx context.Context, token string, req observer.PairingRequest, now int64) (observer.Enrollment, error) {
	return p.enroll(ctx, token, req, now, false)
}
func (p *PairingService) Activate(ctx context.Context, token string, req observer.PairingRequest, now int64) (observer.Enrollment, error) {
	return p.enroll(ctx, token, req, now, true)
}
func (p *PairingService) enroll(ctx context.Context, token string, req observer.PairingRequest, now int64, activate bool) (observer.Enrollment, error) {
	if req.SchemaVersion != 1 || !observer.ValidUUID(req.EnrollmentID) || !observer.ValidSecret(req.ResumeSecret) {
		return observer.Enrollment{}, ErrInput
	}
	hint, err := p.charge(ctx, token, now)
	if err != nil {
		return observer.Enrollment{}, err
	}
	var result observer.Enrollment
	err = p.transaction(ctx, func(tx *gorm.DB) error {
		g, err := p.lockGrant(tx, token, hint, now)
		if err != nil {
			return err
		}
		if g.EnrollmentID == "" {
			if activate {
				return ErrEnrollment
			}
			if err := p.issue(tx, &g, req, now); err != nil {
				return err
			}
		} else if g.EnrollmentID != req.EnrollmentID || subtle.ConstantTimeCompare(g.ResumeHash, digestSecret(req.ResumeSecret)) != 1 {
			return ErrEnrollment
		}
		c, err := p.credential(tx, g, now)
		if err != nil {
			return err
		}
		if activate {
			if c.State == "pending" {
				// Key rotation takes effect atomically; no old-key overlap in v1.
				if err := tx.Exec(`UPDATE observer_credentials SET state='revoked',revoked_at_ms=? WHERE environment=? AND receiver_id=? AND key_id<>? AND state<>'revoked'`, now, p.environment, g.ReceiverID, c.KeyID).Error; err != nil {
					return err
				}
				if err := tx.Exec(`UPDATE observer_credentials SET state='active' WHERE key_id=?`, c.KeyID).Error; err != nil {
					return err
				}
			}
		} else {
			key, err := p.cipher.Open(c.CredentialScope, c.EncryptionKeyID, c.KeyCiphertext)
			if err != nil {
				return err
			}
			defer clear(key)
			result.KeyBase64URL = base64.RawURLEncoding.EncodeToString(key)
		}
		result.SchemaVersion = 1
		result.EnrollmentID = g.EnrollmentID
		result.Origin = p.origin
		result.Environment = c.Environment
		result.ReceiverID = c.ReceiverID
		result.Provider = c.Provider
		result.SourceClass = c.SourceClass
		result.DeviceID = c.DeviceID
		result.BindingVersion = c.BindingVersion
		result.KeyID = c.KeyID
		result.NotBeforeMs = c.NotBeforeMs
		result.ExpiresAtMs = c.ExpiresAtMs
		return nil
	})
	if err != nil {
		return observer.Enrollment{}, err
	}
	return result, nil
}
func (p *PairingService) issue(tx *gorm.DB, g *grantRow, req observer.PairingRequest, now int64) error {
	generation, device, active, err := currentBinding(tx, p.environment, g.ReceiverID)
	if err != nil {
		return err
	}
	if generation != g.ExpectedBindingVersion || g.Mode == "rotate" && (!active || device != g.TargetDeviceID) {
		return ErrBinding
	}
	if g.Mode == "replace" {
		if generation == 2147483647 {
			return ErrBinding
		}
		if err := tx.Exec(`UPDATE observer_credentials SET state='revoked',revoked_at_ms=? WHERE environment=? AND receiver_id=? AND state<>'revoked'`, now, p.environment, g.ReceiverID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE observer_bindings SET active=false WHERE environment=? AND receiver_id=? AND active`, p.environment, g.ReceiverID).Error; err != nil {
			return err
		}
		device, err = randomID()
		if err != nil {
			return err
		}
		generation++
		if err := tx.Exec(`INSERT INTO observer_devices(environment,device_id,enabled) VALUES (?,?,true)`, p.environment, device).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO observer_bindings(environment,receiver_id,binding_version,device_id,provider,active) VALUES (?,?,?,?,?,true)`, p.environment, g.ReceiverID, generation, device, g.Provider).Error; err != nil {
			return err
		}
	}
	keyID, err := randomID()
	if err != nil {
		return err
	}
	scope := CredentialScope{KeyID: keyID, Environment: p.environment, ReceiverID: g.ReceiverID, DeviceID: device, BindingVersion: generation, Provider: g.Provider, SourceClass: p.sourceClass}
	material := make([]byte, 32)
	defer clear(material)
	if _, err := rand.Read(material); err != nil {
		return err
	}
	ciphertext, err := p.cipher.Seal(scope, material)
	if err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO observer_credentials(key_id,environment,receiver_id,binding_version,device_id,provider,source_class,encryption_key_id,key_ciphertext,state,not_before_ms,expires_at_ms) VALUES (?,?,?,?,?,?,?,?,?,'pending',?,?)`, keyID, p.environment, g.ReceiverID, generation, device, g.Provider, p.sourceClass, p.cipher.id, ciphertext, now, now+credentialLifetime).Error; err != nil {
		return err
	}
	g.EnrollmentID = req.EnrollmentID
	g.ResumeHash = digestSecret(req.ResumeSecret)
	g.KeyID = keyID
	return tx.Exec(`UPDATE observer_pairing_grants SET enrollment_id=?,resume_hash=?,key_id=? WHERE grant_id=?`, g.EnrollmentID, g.ResumeHash, g.KeyID, g.GrantID).Error
}
func (p *PairingService) RevokeGrant(ctx context.Context, id string) error {
	if !observer.ValidIdentifier(id) {
		return ErrInput
	}
	result := p.db.WithContext(ctx).Exec(`UPDATE observer_pairing_grants SET revoked=true WHERE grant_id=? AND environment=? AND origin=?`, id, p.environment, p.origin)
	if result.Error != nil {
		return ErrStorage
	}
	if result.RowsAffected != 1 {
		return ErrGrant
	}
	return nil
}
func (p *PairingService) RevokeBinding(ctx context.Context, receiver string, version int, now int64) error {
	if !observer.ValidIdentifier(receiver) || version < 1 || !validPairingTime(now) {
		return ErrInput
	}
	return p.transaction(ctx, func(tx *gorm.DB) error {
		if _, err := p.lockReceiver(tx, receiver); err != nil {
			return err
		}
		result := tx.Exec(`UPDATE observer_bindings SET active=false WHERE environment=? AND receiver_id=? AND binding_version=?`, p.environment, receiver, version)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrBinding
		}
		return tx.Exec(`UPDATE observer_credentials SET state='revoked',revoked_at_ms=? WHERE environment=? AND receiver_id=? AND binding_version=? AND state<>'revoked'`, now, p.environment, receiver, version).Error
	})
}
