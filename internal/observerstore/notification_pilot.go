package observerstore

import (
	"context"
	"time"

	"github.com/example/epay-go/internal/accounting"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const NotificationPilotPolicy = "notification-pilot-v1"

type NotificationPilot struct {
	OrderID, ChannelID, MerchantID, AmountMinor                int64
	Environment, ReceiverID, Provider, DeviceID, ParserVersion string
	BindingVersion                                             int
	ArmedAtMs, ExpiresAtMs, ActorID                            int64
	Enabled                                                    bool
}

// ArmNotificationPilot is an operator-only preparation primitive. There is no
// HTTP route, no default rule and no default expiry. A rule must already have
// private real-sample verification; supplying a parser name does not approve it.
// Registration and all history checks serialize with first receipt.
func (p *PairingService) ArmNotificationPilot(ctx context.Context, receiver string, orderID int64, parser string, actor, now, expires int64) (NotificationPilot, error) {
	var result NotificationPilot
	if p == nil || p.sourceClass != "notification" || !observer.ValidIdentifier(receiver) || orderID <= 0 || actor <= 0 || parser == "" || len(parser) > 64 || now < 0 || now > observer.MaxTime || expires <= now || expires-now > 600000 {
		return result, ErrInput
	}
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var environment struct{ Enabled bool }
		if err := tx.Raw(`SELECT enabled FROM observer_environments WHERE environment=? AND source_class='notification' FOR SHARE`, p.environment).Scan(&environment).Error; err != nil {
			return err
		}
		if !environment.Enabled {
			return ErrScope
		}
		var admins int64
		if err := tx.Raw(`SELECT count(*) FROM admins WHERE id=? AND deleted_at IS NULL`, actor).Scan(&admins).Error; err != nil {
			return err
		}
		if admins != 1 {
			return ErrScope
		}
		var binding struct {
			DeviceID       string
			BindingVersion int
		}
		if err := tx.Raw(`SELECT b.device_id,b.binding_version FROM observer_receivers r JOIN observer_bindings b USING(environment,receiver_id,provider) WHERE r.environment=? AND r.receiver_id=? AND r.provider='alipay' AND r.enabled AND b.active FOR UPDATE OF r`, p.environment, receiver).Scan(&binding).Error; err != nil {
			return err
		}
		if binding.BindingVersion == 0 {
			return ErrScope
		}
		var order model.Order
		if err := tx.Unscoped().First(&order, orderID).Error; err != nil {
			return err
		}
		result = NotificationPilot{OrderID: order.ID, ChannelID: order.ChannelID, MerchantID: order.MerchantID, AmountMinor: order.Amount.Shift(2).IntPart(), Environment: p.environment, ReceiverID: receiver, Provider: "alipay", DeviceID: binding.DeviceID, BindingVersion: binding.BindingVersion, ParserVersion: parser, ArmedAtMs: now, ExpiresAtMs: expires, ActorID: actor, Enabled: true}
		// The INSERT trigger independently checks source, binding, active rule,
		// original order, deadline, channel, merchant and all amount history.
		return tx.Table("observer_notification_pilots").Create(&result).Error
	})
	if err != nil {
		return NotificationPilot{}, err
	}
	return result, nil
}

func notificationPilot(tx *gorm.DB, e eventRow) (*NotificationPilot, error) {
	if e.SourceClass != "notification" {
		return nil, nil
	}
	var p NotificationPilot
	found := tx.Raw(`SELECT * FROM observer_notification_pilots WHERE environment=? AND receiver_id=? AND provider=? AND amount_minor=? FOR SHARE`, e.Environment, e.ReceiverID, e.Provider, e.AmountMinor).Scan(&p)
	if found.Error != nil {
		return nil, found.Error
	}
	if found.RowsAffected != 1 {
		return nil, nil
	}
	return &p, nil
}

func pilotCandidates(tx *gorm.DB, e eventRow, p *NotificationPilot) (int64, *int64, error) {
	var row struct {
		Count  int64
		OnlyID *int64
	}
	err := tx.Raw(`SELECT count(*) AS count,CASE WHEN count(*)=1 THEN min(id) END AS only_id FROM orders WHERE channel_id=? AND pay_type=? AND amount*100=? AND created_at<=?`, p.ChannelID, p.Provider, e.AmountMinor, time.UnixMilli(e.FirstReceivedAtMs)).Scan(&row).Error
	return row.Count, row.OnlyID, err
}

// Called only by first authenticated receipt, inside its outer transaction and
// receiver lock. Old review events can never enter this path on replay.
func settleNotificationPilot(tx *gorm.DB, e eventRow, p *NotificationPilot, count int64, only *int64, paused bool) error {
	d := MatchingDecision{EventPK: e.ID, PolicyVersion: NotificationPilotPolicy, CandidateCount: count, OrderID: only, CandidateCutoffMs: e.FirstReceivedAtMs, DecidedAtMs: e.FirstReceivedAtMs}
	reject := func(outcome, reason string) error {
		d.Outcome, d.Reason = outcome, reason
		return persistDecision(tx, d, 0)
	}
	if paused {
		return reject("evidence_insufficient", "confirmation_paused")
	}
	if !p.Enabled || e.Provider != "alipay" || e.DeviceID != p.DeviceID || e.BindingVersion != p.BindingVersion {
		return reject("evidence_insufficient", "scope_changed")
	}
	if e.IdentityConfidence != "distinct" || e.ParserVersion != p.ParserVersion || e.ProviderTransactionID != nil {
		return reject("evidence_insufficient", "unqualified_evidence")
	}
	var rule struct{ Enabled bool }
	if err := tx.Raw(`SELECT enabled FROM observer_notification_rules WHERE parser_version=? AND provider=? FOR SHARE`, p.ParserVersion, e.Provider).Scan(&rule).Error; err != nil {
		return err
	}
	if !rule.Enabled {
		return reject("evidence_insufficient", "unqualified_evidence")
	}
	if e.ProcessingStatus != "pending" {
		return reject("evidence_insufficient", "preexisting_review")
	}
	if e.FirstReceivedAtMs-e.ObservedAtMs < -2000 {
		return reject("clock_error", "clock_skew")
	}
	if e.FirstReceivedAtMs-e.ObservedAtMs > 120000 {
		return reject("stale_observation", "stale_observation")
	}
	if e.ObservedAtMs < p.ArmedAtMs || e.FirstReceivedAtMs < p.ArmedAtMs {
		return reject("clock_error", "observed_before_order")
	}
	if e.ObservedAtMs >= p.ExpiresAtMs || e.FirstReceivedAtMs >= p.ExpiresAtMs {
		return reject("expired", "window_expired")
	}
	var prior int64
	if err := tx.Raw(`SELECT count(*) FROM observer_events WHERE environment=? AND receiver_id=? AND provider=? AND amount_minor=? AND id<?`, e.Environment, e.ReceiverID, e.Provider, e.AmountMinor, e.ID).Scan(&prior).Error; err != nil {
		return err
	}
	if prior != 0 {
		return reject("duplicate_suspected", "earlier_evidence")
	}
	var receipts int64
	if err := tx.Raw(`SELECT count(*) FROM observer_requests WHERE event_pk=? AND key_id=? AND request_id=? AND disposition='stored'`, e.ID, e.FirstKeyID, e.FirstRequestID).Scan(&receipts).Error; err != nil {
		return err
	}
	if receipts != 1 {
		return reject("evidence_insufficient", "missing_receipt")
	}
	if count == 0 {
		return reject("unmatched", "no_candidate")
	}
	if count != 1 {
		return reject("ambiguous", "multiple_candidates")
	}
	if only == nil || *only != p.OrderID {
		return reject("evidence_insufficient", "candidate_changed")
	}
	// Lock channel before order/merchant, matching checkout creation's ordering.
	var channel model.Channel
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "SHARE"}).First(&channel, p.ChannelID).Error; err != nil {
		return err
	}
	if channel.Plugin != "epay" || channel.Status != 1 || channel.DeletedAt.Valid {
		return reject("evidence_insufficient", "scope_changed")
	}
	var order model.Order
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, p.OrderID).Error; err != nil {
		return err
	}
	if order.Status == model.OrderStatusPaid || order.Status == model.OrderStatusRefund {
		return reject("duplicate_suspected", "order_settled")
	}
	if order.DeletedAt.Valid || order.Status != model.OrderStatusUnpaid || order.MerchantID != p.MerchantID || order.ChannelID != p.ChannelID || order.PayType != p.Provider || order.Amount.Shift(2).IntPart() != p.AmountMinor || !order.Amount.Equal(order.RealAmount) || (order.CheckoutExpiresAt != nil && order.CheckoutExpiresAt.UnixMilli() < p.ExpiresAtMs) {
		return reject("evidence_insufficient", "scope_changed")
	}
	var merchant model.Merchant
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&merchant, p.MerchantID).Error; err != nil {
		return err
	}
	if merchant.Status != 1 || merchant.DeletedAt.Valid {
		return reject("evidence_insufficient", "scope_changed")
	}
	ledger, err := accounting.CreditLockedOrder(tx, &order, nil, "", time.UnixMilli(e.FirstReceivedAtMs))
	if err != nil {
		return err
	}
	d.Outcome, d.Reason = "matched", "eligible"
	return persistDecision(tx, d, ledger)
}
