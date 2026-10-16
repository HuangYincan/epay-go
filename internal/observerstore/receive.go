package observerstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
)

type ReceiveResult struct {
	Status            int
	Code              string
	RetryAfterSeconds int
	Receipt           *observer.Receipt
}

func rejectedRequest(status int, code string) ReceiveResult {
	return ReceiveResult{Status: status, Code: code}
}

type Receiver struct{ authority *PairingService }

func NewReceiver(authority *PairingService) *Receiver { return &Receiver{authority: authority} }

// Receive returns a successful receipt only after the outer transaction commits.
// TLS and body-size admission also belong to the HTTP boundary. This transaction
// serializes with pairing/revocation on the stable receiver, never on an order.
func (receiver *Receiver) Receive(ctx context.Context, r *http.Request, body []byte, now int64) ReceiveResult {
	p := receiver.authority
	if p == nil || now < 0 || now > observer.MaxTime {
		return rejectedRequest(503, "unavailable")
	}
	keyID, ok := observer.RequestKeyID(r)
	if !ok {
		return rejectedRequest(401, "unknown_key")
	}
	var hint credentialRow
	found := p.db.WithContext(ctx).Raw(`SELECT * FROM observer_credentials WHERE key_id=?`, keyID).Scan(&hint)
	if found.Error != nil {
		return rejectedRequest(503, "unavailable")
	}
	if found.RowsAffected != 1 {
		return rejectedRequest(401, "unknown_key")
	}
	if hint.Environment != p.environment || hint.SourceClass != p.sourceClass {
		return rejectedRequest(403, "environment_denied")
	}
	result := rejectedRequest(503, "unavailable")
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var environment struct {
			Enabled     bool
			SourceClass string
		}
		row := tx.Raw(`SELECT enabled,source_class FROM observer_environments WHERE environment=? FOR SHARE`, p.environment).Scan(&environment)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 || !environment.Enabled || environment.SourceClass != p.sourceClass {
			result = rejectedRequest(403, "environment_denied")
			return nil
		}
		var account struct {
			Enabled  bool
			Provider string
		}
		row = tx.Raw(`SELECT enabled,provider FROM observer_receivers WHERE environment=? AND receiver_id=? FOR UPDATE`, p.environment, hint.ReceiverID).Scan(&account)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 || !account.Enabled || account.Provider != hint.Provider {
			result = rejectedRequest(403, "scope_denied")
			return nil
		}
		var device struct{ Enabled bool }
		row = tx.Raw(`SELECT enabled FROM observer_devices WHERE environment=? AND device_id=? FOR SHARE`, p.environment, hint.DeviceID).Scan(&device)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 || !device.Enabled {
			result = rejectedRequest(403, "revoked")
			return nil
		}
		var binding struct{ Active bool }
		row = tx.Raw(`SELECT active FROM observer_bindings WHERE environment=? AND receiver_id=? AND binding_version=? AND device_id=? AND provider=? FOR SHARE`, p.environment, hint.ReceiverID, hint.BindingVersion, hint.DeviceID, hint.Provider).Scan(&binding)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 || !binding.Active {
			result = rejectedRequest(403, "binding_changed")
			return nil
		}
		var credential credentialRow
		row = tx.Raw(`SELECT * FROM observer_credentials WHERE key_id=? FOR UPDATE`, keyID).Scan(&credential)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 {
			return ErrStorage
		}
		if credential.State == "revoked" {
			result = rejectedRequest(403, "revoked")
			return nil
		}
		if credential.State != "active" {
			result = rejectedRequest(403, "scope_denied")
			return nil
		}
		if now < credential.NotBeforeMs || now >= credential.ExpiresAtMs {
			result = rejectedRequest(401, "unknown_key")
			return nil
		}
		key, err := p.cipher.Open(credential.CredentialScope, credential.EncryptionKeyID, credential.KeyCiphertext)
		if err != nil {
			return err
		}
		verified, err := observer.VerifyRequest(r, body, keyID, key, now)
		clear(key)
		if err != nil {
			result = verificationFailure(err.Error())
			return nil
		}
		event := verified.Event
		if event.DeviceID != credential.DeviceID || event.ReceiverID != credential.ReceiverID || event.BindingVersion != int64(credential.BindingVersion) || event.Provider != credential.Provider || event.SourceClass != credential.SourceClass {
			result = rejectedRequest(403, "scope_denied")
			return nil
		}
		retry, err := chargeDevice(tx, credential.Environment, credential.DeviceID, now)
		if err != nil {
			return err
		}
		if retry > 0 {
			result = rejectedRequest(429, "rate_limited")
			result.RetryAfterSeconds = retry
			return nil
		}
		digest := sha256.Sum256([]byte(strings.Join([]string{"epay-observer-request-v1", keyID, strconv.FormatInt(verified.SentAt, 10), verified.RequestID, verified.BodySHA256}, "\n")))
		envelope := hex.EncodeToString(digest[:])
		var request struct {
			EnvelopeSHA256 string
			EventPK        int64
			Disposition    string
		}
		row = tx.Raw(`SELECT envelope_sha256,event_pk,disposition FROM observer_requests WHERE key_id=? AND request_id=?`, keyID, verified.RequestID).Scan(&request)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected == 1 {
			if request.EnvelopeSHA256 != envelope {
				result = rejectedRequest(409, "request_conflict")
				return nil
			}
			if request.Disposition == "conflict" {
				result = rejectedRequest(409, "event_conflict")
				return nil
			}
			result, err = storedReceipt(tx, request.EventPK, false)
			return err
		}
		evidence, err := RecordEvidence(ctx, tx, p.environment, keyID, verified, now)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO observer_requests(key_id,request_id,envelope_sha256,event_pk,disposition,first_seen_at_ms) VALUES (?,?,?,?,?,?)`, keyID, verified.RequestID, envelope, evidence.EventPK, evidence.Disposition, now).Error; err != nil {
			return err
		}
		if evidence.Disposition == "conflict" {
			result = rejectedRequest(409, "event_conflict")
			return nil
		}
		result, err = storedReceipt(tx, evidence.EventPK, evidence.Disposition == "stored")
		return err
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return rejectedRequest(503, "unavailable")
	}
	return result
}
func verificationFailure(code string) ReceiveResult {
	switch code {
	case "invalid_signature", "clock_skew", "unknown_key":
		return rejectedRequest(401, code)
	case "body_too_large":
		return rejectedRequest(413, code)
	case "unsupported_media_type":
		return rejectedRequest(415, code)
	case "invalid_route", "invalid_request", "unsupported_version", "invalid_payload":
		return rejectedRequest(400, code)
	default:
		return rejectedRequest(400, "invalid_payload")
	}
}
func chargeDevice(tx *gorm.DB, environment, device string, now int64) (int, error) {
	if err := tx.Exec(`INSERT INTO observer_device_budgets(environment,device_id,tokens_milli,last_refill_ms) VALUES (?,?,20000,?) ON CONFLICT(environment,device_id) DO NOTHING`, environment, device, now).Error; err != nil {
		return 0, err
	}
	var budget struct {
		TokensMilli  int64
		LastRefillMs int64
	}
	row := tx.Raw(`SELECT tokens_milli,last_refill_ms FROM observer_device_budgets WHERE environment=? AND device_id=? FOR UPDATE`, environment, device).Scan(&budget)
	if row.Error != nil {
		return 0, row.Error
	}
	if row.RowsAffected != 1 {
		return 0, ErrStorage
	}
	if now > budget.LastRefillMs {
		elapsed := now - budget.LastRefillMs
		if elapsed >= 20000 {
			budget.TokensMilli = 20000
		} else {
			budget.TokensMilli = min(int64(20000), budget.TokensMilli+elapsed)
		}
		budget.LastRefillMs = now
	}
	retry := 0
	if budget.TokensMilli >= 1000 {
		budget.TokensMilli -= 1000
	} else {
		retry = 1
	}
	err := tx.Exec(`UPDATE observer_device_budgets SET tokens_milli=?,last_refill_ms=? WHERE environment=? AND device_id=?`, budget.TokensMilli, budget.LastRefillMs, environment, device).Error
	return retry, err
}
func storedReceipt(tx *gorm.DB, eventPK int64, first bool) (ReceiveResult, error) {
	var event struct {
		EventID, ProcessingStatus string
		FirstReceivedAtMs         int64
	}
	row := tx.Raw(`SELECT event_id,processing_status,first_received_at_ms FROM observer_events WHERE id=?`, eventPK).Scan(&event)
	if row.Error != nil {
		return ReceiveResult{}, row.Error
	}
	if row.RowsAffected != 1 {
		return ReceiveResult{}, ErrStorage
	}
	status, receipt := 200, "duplicate"
	if first {
		status = 202
		receipt = "accepted"
	}
	return ReceiveResult{Status: status, Receipt: &observer.Receipt{SchemaVersion: 1, EventID: event.EventID, ReceiptStatus: receipt, ProcessingStatus: event.ProcessingStatus, ReceivedAtMs: event.FirstReceivedAtMs}}, nil
}
