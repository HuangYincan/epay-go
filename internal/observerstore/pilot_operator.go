package observerstore

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Review facts are operator input, never inferred from a parser name or readiness check.
type PilotOperatorInput struct {
	SchemaVersion          int    `json:"schema_version"`
	Origin                 string `json:"origin"`
	Environment            string `json:"environment"`
	ReceiverID             string `json:"receiver_id"`
	OrderID                int64  `json:"order_id"`
	ActorID                int64  `json:"actor_id"`
	ExpiresAtMs            int64  `json:"expires_at_ms"`
	ParserVersion          string `json:"parser_version"`
	EvidenceRef            string `json:"evidence_ref"`
	TemplateVerified       int    `json:"template_verified"`
	ChatBoundaryVerified   int    `json:"chat_boundary_verified"`
	IdentityPolicyApproved int    `json:"identity_policy_approved"`
	AmountPolicyApproved   int    `json:"amount_policy_approved"`
	NewAPIContractVerified int    `json:"newapi_contract_verified"`
}

const PilotOperatorParserVersion = "alipay-12.12.30-receipt-v1"

var operatorFields = []string{"schema_version", "origin", "environment", "receiver_id", "order_id", "actor_id", "expires_at_ms", "parser_version", "evidence_ref", "template_verified", "chat_boundary_verified", "identity_policy_approved", "amount_policy_approved", "newapi_contract_verified"}

func DecodePilotOperatorInput(body []byte) (PilotOperatorInput, error) {
	var in PilotOperatorInput
	if len(body) == 0 || len(body) > 4096 {
		return in, ErrInput
	}
	if _, err := observer.StrictObject(body, operatorFields); err != nil {
		return in, ErrInput
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return PilotOperatorInput{}, ErrInput
	}
	if in.SchemaVersion != 1 || !observer.ValidOrigin(in.Origin) || !observer.ValidIdentifier(in.Environment) || !observer.ValidIdentifier(in.ReceiverID) ||
		!observer.ValidIdentifier(in.EvidenceRef) || in.ParserVersion != PilotOperatorParserVersion || in.OrderID <= 0 || in.ActorID <= 0 || in.ExpiresAtMs <= 0 || in.ExpiresAtMs > observer.MaxTime {
		return PilotOperatorInput{}, ErrInput
	}
	for _, v := range []int{in.TemplateVerified, in.ChatBoundaryVerified, in.IdentityPolicyApproved, in.AmountPolicyApproved, in.NewAPIContractVerified} {
		if v < 0 || v > 1 {
			return PilotOperatorInput{}, ErrInput
		}
	}
	return in, nil
}
func (in PilotOperatorInput) ReviewComplete() bool {
	return in.TemplateVerified == 1 && in.ChatBoundaryVerified == 1 && in.IdentityPolicyApproved == 1 && in.AmountPolicyApproved == 1 && in.NewAPIContractVerified == 1
}
func (in PilotOperatorInput) Preflight(now int64) PilotPreflightInput {
	return PilotPreflightInput{Environment: in.Environment, ReceiverID: in.ReceiverID, ParserVersion: in.ParserVersion, OrderID: in.OrderID, ActorID: in.ActorID, NowMs: now, ExpiresAtMs: in.ExpiresAtMs}
}

// PilotHandoff is exactly SPEC-041's flat Android configuration. No order hints or secrets.
type PilotHandoff struct {
	SchemaVersion          int    `json:"schema_version"`
	Origin                 string `json:"origin"`
	Environment            string `json:"environment"`
	DeviceID               string `json:"device_id"`
	ReceiverID             string `json:"receiver_id"`
	BindingVersion         int    `json:"binding_version"`
	Provider               string `json:"provider"`
	SourceClass            string `json:"source_class"`
	ParserVersion          string `json:"parser_version"`
	AmountMinor            int64  `json:"amount_minor"`
	ServerArmedAtMs        int64  `json:"server_armed_at_ms"`
	ServerExpiresAtMs      int64  `json:"server_expires_at_ms"`
	EvidenceRef            string `json:"evidence_ref"`
	TemplateVerified       int    `json:"template_verified"`
	ChatBoundaryVerified   int    `json:"chat_boundary_verified"`
	IdentityPolicyApproved int    `json:"identity_policy_approved"`
	AmountPolicyApproved   int    `json:"amount_policy_approved"`
	NewAPIContractVerified int    `json:"newapi_contract_verified"`
}

func verifiedPilotOrigin(tx *gorm.DB, in PilotOperatorInput, now int64) (bool, error) {
	var origins struct {
		Count   int64
		Matches bool
	}
	e := tx.Raw(`SELECT count(DISTINCT g.origin) AS count,coalesce(bool_and(g.origin=?),false) AS matches FROM observer_credentials c JOIN observer_bindings b USING(environment,receiver_id,provider,device_id,binding_version) JOIN observer_pairing_grants g ON g.key_id=c.key_id WHERE c.environment=? AND c.receiver_id=? AND c.provider='alipay' AND c.source_class='notification' AND b.active AND c.state='active' AND c.not_before_ms<=? AND c.expires_at_ms>=?`, in.Origin, in.Environment, in.ReceiverID, now, in.ExpiresAtMs).Scan(&origins).Error
	return origins.Count == 1 && origins.Matches, e
}

// InspectPilotOperator adds review and paired-origin checks without writing. The
// snapshots are advisory; actual registration repeats origin under receiver lock.
func InspectPilotOperator(ctx context.Context, db *gorm.DB, in PilotOperatorInput, now int64) (PilotPreflight, error) {
	raw, e := json.Marshal(in)
	if e != nil {
		return PilotPreflight{}, ErrInput
	}
	if _, e := DecodePilotOperatorInput(raw); e != nil {
		return PilotPreflight{}, ErrInput
	}
	report, e := InspectNotificationPilot(ctx, db, in.Preflight(now))
	if e != nil {
		return PilotPreflight{}, e
	}
	if !in.ReviewComplete() {
		report.Blockers = append(report.Blockers, "review_incomplete")
	}
	for _, b := range report.Blockers {
		if b == "schema_not_ready" {
			report.Ready = false
			return report, nil
		}
	}
	var origin bool
	e = db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var err error
		origin, err = verifiedPilotOrigin(tx, in, now)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return PilotPreflight{}, e
	}
	if !origin {
		report.Blockers = append(report.Blockers, "pairing_origin_mismatch")
	}
	report.Ready = len(report.Blockers) == 0
	return report, nil
}

// PrepareReviewedNotificationPilot is local operator authority, not a public endpoint.
// It neither migrates nor decrypts credentials. The old Arm and DB triggers remain decisive.
// Repeating the exact request only recovers the original fixed window, never renews it.
func PrepareReviewedNotificationPilot(ctx context.Context, db *gorm.DB, in PilotOperatorInput, now int64) (handoff PilotHandoff, recovered bool, err error) {
	encoded, e := json.Marshal(in)
	if e != nil {
		return handoff, false, ErrInput
	}
	if _, e = DecodePilotOperatorInput(encoded); e != nil || !in.ReviewComplete() {
		return handoff, false, ErrInput
	}
	report, e := InspectPilotOperator(ctx, db, in, now)
	if e != nil {
		return handoff, false, publicError(e)
	}
	for _, block := range report.Blockers {
		if block != "rule_unverified" && block != "pilot_conflict" {
			return handoff, false, ErrScope
		}
	}
	err = db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		p := &PairingService{db: tx, environment: in.Environment, sourceClass: "notification", origin: in.Origin}
		provider, e := p.lockReceiver(tx, in.ReceiverID)
		if e != nil {
			return e
		}
		if provider != "alipay" {
			return ErrScope
		}
		var admins int64
		if e := tx.Raw(`SELECT count(*) FROM (SELECT id FROM admins WHERE id=? AND deleted_at IS NULL FOR SHARE) a`, in.ActorID).Scan(&admins).Error; e != nil {
			return e
		}
		if admins != 1 {
			return ErrScope
		}
		var paused bool
		if e := tx.Raw(`SELECT EXISTS(SELECT 1 FROM observer_confirmation_controls WHERE environment=? AND receiver_id=? AND paused)`, in.Environment, in.ReceiverID).Scan(&paused).Error; e != nil {
			return e
		}
		if paused {
			return ErrScope
		}
		origin, e := verifiedPilotOrigin(tx, in, now)
		if e != nil {
			return e
		}
		if !origin {
			return ErrScope
		}
		var rule struct {
			EvidenceRef string
			ApprovedBy  int64
			Enabled     bool
		}
		found := tx.Raw(`SELECT evidence_ref,approved_by,enabled FROM observer_notification_rules WHERE parser_version=? AND provider='alipay' FOR SHARE`, in.ParserVersion).Scan(&rule)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected == 0 {
			if e := tx.Exec(`INSERT INTO observer_notification_rules(parser_version,provider,evidence_ref,approved_by,enabled) VALUES (?,'alipay',?,?,true)`, in.ParserVersion, in.EvidenceRef, in.ActorID).Error; e != nil {
				return e
			}
		} else if !rule.Enabled || rule.EvidenceRef != in.EvidenceRef || rule.ApprovedBy != in.ActorID {
			return ErrScope
		}
		var pilot NotificationPilot
		existing := tx.Raw(`SELECT * FROM observer_notification_pilots WHERE order_id=? FOR SHARE`, in.OrderID).Scan(&pilot)
		if existing.Error != nil {
			return existing.Error
		}
		if existing.RowsAffected == 1 {
			if !pilot.Enabled || pilot.Environment != in.Environment || pilot.ReceiverID != in.ReceiverID || pilot.ParserVersion != in.ParserVersion || pilot.ActorID != in.ActorID || pilot.ExpiresAtMs != in.ExpiresAtMs || now < pilot.ArmedAtMs || now >= pilot.ExpiresAtMs {
				return ErrScope
			}
			version, device, active, e := currentBinding(tx, in.Environment, in.ReceiverID)
			if e != nil {
				return e
			}
			if !active || version != pilot.BindingVersion || device != pilot.DeviceID {
				return ErrScope
			}
			lockedReport := PilotPreflight{Blockers: []string{}}
			if e := inspectNotificationPilotSnapshot(tx, in.Preflight(now), &lockedReport); e != nil {
				return e
			}
			for _, block := range lockedReport.Blockers {
				if block != "pilot_conflict" {
					return ErrScope
				}
			}
			recovered = true
		} else {
			var e error
			pilot, e = p.ArmNotificationPilot(ctx, in.ReceiverID, in.OrderID, in.ParserVersion, in.ActorID, now, in.ExpiresAtMs)
			if e != nil {
				return e
			}
		}
		handoff = PilotHandoff{SchemaVersion: 1, Origin: in.Origin, Environment: pilot.Environment, DeviceID: pilot.DeviceID, ReceiverID: pilot.ReceiverID, BindingVersion: pilot.BindingVersion, Provider: "alipay", SourceClass: "notification", ParserVersion: pilot.ParserVersion, AmountMinor: pilot.AmountMinor, ServerArmedAtMs: pilot.ArmedAtMs, ServerExpiresAtMs: pilot.ExpiresAtMs, EvidenceRef: in.EvidenceRef, TemplateVerified: in.TemplateVerified, ChatBoundaryVerified: in.ChatBoundaryVerified, IdentityPolicyApproved: in.IdentityPolicyApproved, AmountPolicyApproved: in.AmountPolicyApproved, NewAPIContractVerified: in.NewAPIContractVerified}
		return nil
	})
	if err != nil {
		return PilotHandoff{}, false, publicError(err)
	}
	return handoff, recovered, nil
}
