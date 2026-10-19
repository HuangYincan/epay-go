package observerstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"regexp"

	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// PilotPreflight is an advisory snapshot, never registration or payment authority.
// No secrets, notification text, transaction references or account identifiers are returned.
type PilotPreflight struct {
	Ready    bool     `json:"ready"`
	Blockers []string `json:"blockers"`
}

type PilotPreflightInput struct {
	Environment, ReceiverID, ParserVersion string
	OrderID, ActorID, NowMs, ExpiresAtMs   int64
}

// InspectNotificationPilot never migrates, polls a provider, arms a pilot, or writes.
// The actual registration/receipt transactions must still revalidate all state.
func InspectNotificationPilot(ctx context.Context, db *gorm.DB, in PilotPreflightInput) (PilotPreflight, error) {
	r := PilotPreflight{Blockers: []string{}}
	if db == nil || !observer.ValidIdentifier(in.Environment) || !observer.ValidIdentifier(in.ReceiverID) ||
		!regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`).MatchString(in.ParserVersion) || in.OrderID <= 0 || in.ActorID <= 0 ||
		in.NowMs < 0 || in.NowMs > observer.MaxTime || in.ExpiresAtMs <= in.NowMs ||
		in.ExpiresAtMs > observer.MaxTime || in.ExpiresAtMs-in.NowMs > 600000 {
		return r, ErrInput
	}
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var schemaExists bool
		if err := tx.Raw(`SELECT to_regclass('observer_schema_versions') IS NOT NULL`).Scan(&schemaExists).Error; err != nil {
			return err
		}
		if !schemaExists {
			r.Blockers = append(r.Blockers, "schema_not_ready")
			return nil
		}
		var versions []struct {
			Version  int
			Checksum string
		}
		if err := tx.Raw(`SELECT version,checksum FROM observer_schema_versions ORDER BY version`).Scan(&versions).Error; err != nil {
			return err
		}
		scripts := []string{schemaOne, schemaTwo, schemaThree, schemaFour, schemaFive, schemaSix, schemaSeven, schemaEight}
		validSchema := len(versions) == len(scripts)
		if validSchema {
			for i, script := range scripts {
				digest := sha256.Sum256([]byte(script))
				validSchema = validSchema && versions[i].Version == i+1 && versions[i].Checksum == hex.EncodeToString(digest[:])
			}
		}
		if !validSchema {
			r.Blockers = append(r.Blockers, "schema_not_ready")
			return nil
		}
		check := func(code, query string, args ...any) error {
			var ok bool
			if err := tx.Raw(query, args...).Scan(&ok).Error; err != nil {
				return err
			}
			if !ok {
				r.Blockers = append(r.Blockers, code)
			}
			return nil
		}
		type condition struct {
			code, query string
			args        []any
		}
		conditions := []condition{
			{"environment_unavailable", `SELECT EXISTS(SELECT 1 FROM observer_environments WHERE environment=? AND source_class='notification' AND enabled)`, []any{in.Environment}},
			{"actor_unavailable", `SELECT EXISTS(SELECT 1 FROM admins WHERE id=? AND deleted_at IS NULL)`, []any{in.ActorID}},
			{"binding_unavailable", `SELECT count(*)=1 FROM observer_receivers r JOIN observer_bindings b USING(environment,receiver_id,provider) JOIN observer_devices d USING(environment,device_id) WHERE r.environment=? AND r.receiver_id=? AND r.provider='alipay' AND r.enabled AND b.active AND d.enabled`, []any{in.Environment, in.ReceiverID}},
			{"credential_unavailable", `SELECT EXISTS(SELECT 1 FROM observer_credentials c JOIN observer_bindings b USING(environment,receiver_id,provider,device_id,binding_version) WHERE c.environment=? AND c.receiver_id=? AND c.provider='alipay' AND b.active AND c.source_class='notification' AND c.state='active' AND c.not_before_ms<=? AND c.expires_at_ms>=?)`, []any{in.Environment, in.ReceiverID, in.NowMs, in.ExpiresAtMs}},
			{"rule_unverified", `SELECT EXISTS(SELECT 1 FROM observer_notification_rules WHERE parser_version=? AND provider='alipay' AND enabled)`, []any{in.ParserVersion}},
			{"order_unavailable", `SELECT EXISTS(SELECT 1 FROM orders WHERE id=? AND pay_type='alipay' AND status=0 AND deleted_at IS NULL AND amount>0 AND amount*100 BETWEEN 1 AND 999999999999 AND amount*100=trunc(amount*100) AND real_amount=amount AND observer_pay_expires_at IS NULL AND notify_url<>'' AND checkout_type='qrcode' AND pay_url<>'' AND created_at<=to_timestamp(?::numeric/1000) AND (checkout_expires_at IS NULL OR checkout_expires_at>=to_timestamp(?::numeric/1000)))`, []any{in.OrderID, in.NowMs, in.ExpiresAtMs}},
			{"channel_unavailable", `SELECT EXISTS(SELECT 1 FROM orders o JOIN channels c ON c.id=o.channel_id WHERE o.id=? AND c.plugin='epay' AND c.status=1 AND c.deleted_at IS NULL)`, []any{in.OrderID}},
			{"merchant_unavailable", `SELECT EXISTS(SELECT 1 FROM orders o JOIN merchants m ON m.id=o.merchant_id WHERE o.id=? AND m.status=1 AND m.deleted_at IS NULL)`, []any{in.OrderID}},
			{"amount_history_conflict", `SELECT NOT EXISTS(SELECT 1 FROM orders o JOIN orders h ON h.channel_id=o.channel_id AND h.pay_type=o.pay_type AND h.amount=o.amount AND h.id<>o.id WHERE o.id=?)`, []any{in.OrderID}},
			{"earlier_evidence", `SELECT NOT EXISTS(SELECT 1 FROM orders o JOIN observer_events e ON e.amount_minor=o.amount*100 WHERE o.id=? AND e.environment=? AND e.receiver_id=? AND e.provider='alipay')`, []any{in.OrderID, in.Environment, in.ReceiverID}},
			{"alias_conflict", `SELECT NOT EXISTS(SELECT 1 FROM orders o JOIN observer_channel_aliases a ON a.channel_id=o.channel_id WHERE o.id=?)`, []any{in.OrderID}},
			{"pilot_conflict", `SELECT NOT EXISTS(SELECT 1 FROM orders o JOIN observer_notification_pilots p ON p.order_id=o.id OR (p.channel_id=o.channel_id AND p.provider=o.pay_type AND p.amount_minor=o.amount*100) OR (p.environment=? AND p.receiver_id=? AND (p.amount_minor=o.amount*100 OR (p.enabled AND p.expires_at_ms>?))) OR (p.channel_id=o.channel_id AND ROW(p.environment,p.receiver_id,p.provider) IS DISTINCT FROM ROW(?::varchar,?::varchar,'alipay'::text)) WHERE o.id=?)`, []any{in.Environment, in.ReceiverID, in.NowMs, in.Environment, in.ReceiverID, in.OrderID}},
			{"confirmation_paused", `SELECT NOT EXISTS(SELECT 1 FROM observer_confirmation_controls WHERE environment=? AND receiver_id=? AND paused)`, []any{in.Environment, in.ReceiverID}},
		}
		for _, c := range conditions {
			if err := check(c.code, c.query, c.args...); err != nil {
				return err
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		// A partial report is never usable after a database/commit failure.
		return PilotPreflight{}, ErrStorage
	}
	r.Ready = len(r.Blockers) == 0
	return r, nil
}
