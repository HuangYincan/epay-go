package observerstore

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/example/epay-go/internal/accounting"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MatchingDecision struct {
	EventPK                           int64
	PolicyVersion, Outcome, Reason    string
	CandidateCount, CandidateCutoffMs int64
	OrderID                           *int64
	DecidedAtMs                       int64
}

type Matcher struct {
	db          *gorm.DB
	environment string
}

func NewMatcher(db *gorm.DB, environment string) (*Matcher, error) {
	if db == nil || environment == "" || environment != SyntheticEnvironment() {
		return nil, ErrSyntheticDisabled
	}
	return &Matcher{db: db, environment: environment}, nil
}

// ProcessEvent takes only a persisted evidence ID. Candidate IDs come exclusively
// from the server's first-receipt snapshot, never the device or caller.
func (m *Matcher) ProcessEvent(ctx context.Context, eventPK, now int64) (MatchingDecision, error) {
	if m == nil || m.environment == "" || m.environment != SyntheticEnvironment() {
		return MatchingDecision{}, ErrSyntheticDisabled
	}
	if eventPK <= 0 || now < 0 || now > observer.MaxTime {
		return MatchingDecision{}, ErrInput
	}
	var hint eventRow
	found := m.db.WithContext(ctx).Raw(`SELECT * FROM observer_events WHERE id=?`, eventPK).Scan(&hint)
	if found.Error != nil {
		return MatchingDecision{}, found.Error
	}
	if found.RowsAffected != 1 || hint.Environment != m.environment {
		return MatchingDecision{}, ErrScope
	}
	var decision MatchingDecision
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		authorized, err := lockMatchingAuthority(tx, hint, now)
		if err != nil {
			return err
		}
		var claim struct{ OrderID, ChannelID, MerchantID, CreatedAtMs, ObserverPayExpiresAtMs int64 }
		claimResult := tx.Raw(`SELECT order_id,channel_id,merchant_id,created_at_ms,observer_pay_expires_at_ms FROM observer_amount_claims WHERE environment=? AND receiver_id=? AND provider=? AND amount_minor=? FOR UPDATE`, hint.Environment, hint.ReceiverID, hint.Provider, hint.AmountMinor).Scan(&claim)
		if claimResult.Error != nil {
			return claimResult.Error
		}
		var event eventRow
		if err := tx.Raw(`SELECT * FROM observer_events WHERE id=? FOR UPDATE`, eventPK).Scan(&event).Error; err != nil {
			return err
		}
		previous := tx.Raw(`SELECT * FROM observer_match_decisions WHERE event_pk=?`, eventPK).Scan(&decision)
		if previous.Error != nil {
			return previous.Error
		}
		if previous.RowsAffected == 1 {
			return nil
		}
		decision = MatchingDecision{EventPK: eventPK, PolicyVersion: SyntheticPolicy, CandidateCutoffMs: event.FirstReceivedAtMs, DecidedAtMs: now}
		reject := func(outcome, reason string) error {
			decision.Outcome = outcome
			decision.Reason = reason
			return persistDecision(tx, decision, 0)
		}
		var snapshot candidateSnapshot
		snapshotResult := tx.Raw(`SELECT * FROM observer_candidate_snapshots WHERE event_pk=?`, eventPK).Scan(&snapshot)
		if snapshotResult.Error != nil {
			return snapshotResult.Error
		}
		decision.CandidateCount = snapshot.CandidateCount
		decision.OrderID = snapshot.CandidateOrderID
		if !authorized {
			return reject("evidence_insufficient", "scope_changed")
		}
		if event.ProcessingStatus != "pending" {
			return reject("evidence_insufficient", "preexisting_review")
		}
		parser := "fixture-wx-v1"
		if event.Provider == "alipay" {
			parser = "fixture-ali-v1"
		}
		if event.SourceClass != "synthetic" || event.IdentityConfidence != "distinct" || event.ParserVersion != parser || event.ProviderTransactionID != nil {
			return reject("evidence_insufficient", "unqualified_evidence")
		}
		var received int64
		if err := tx.Raw(`SELECT count(*) FROM observer_requests WHERE event_pk=? AND key_id=? AND request_id=? AND disposition='stored'`, event.ID, event.FirstKeyID, event.FirstRequestID).Scan(&received).Error; err != nil {
			return err
		}
		if received != 1 {
			return reject("evidence_insufficient", "missing_receipt")
		}
		if snapshotResult.RowsAffected != 1 || snapshot.CandidateCutoffMs != event.FirstReceivedAtMs {
			return reject("evidence_insufficient", "missing_snapshot")
		}
		var conflicts int64
		if err := tx.Table("observer_event_conflicts").Where("event_pk=?", event.ID).Count(&conflicts).Error; err != nil {
			return err
		}
		if conflicts != 0 {
			return reject("evidence_insufficient", "event_conflict")
		}
		if now < event.FirstReceivedAtMs || event.FirstReceivedAtMs-event.ObservedAtMs < -2000 {
			return reject("clock_error", "clock_skew")
		}
		if event.FirstReceivedAtMs-event.ObservedAtMs > 120000 {
			return reject("stale_observation", "stale_observation")
		}
		var prior int64
		if err := tx.Raw(`SELECT count(*) FROM observer_events WHERE environment=? AND receiver_id=? AND provider=? AND amount_minor=? AND id<?`, event.Environment, event.ReceiverID, event.Provider, event.AmountMinor, event.ID).Scan(&prior).Error; err != nil {
			return err
		}
		if prior != 0 {
			return reject("duplicate_suspected", "earlier_evidence")
		}
		if snapshot.CandidateCount == 0 {
			return reject("unmatched", "no_candidate")
		}
		if snapshot.CandidateCount > 1 {
			return reject("ambiguous", "multiple_candidates")
		}
		count, only, err := currentCandidates(tx, event)
		if err != nil {
			return err
		}
		if count != 1 || only == nil || snapshot.CandidateOrderID == nil || *only != *snapshot.CandidateOrderID {
			return reject("evidence_insufficient", "candidate_changed")
		}
		if claimResult.RowsAffected != 1 || snapshot.ClaimOrderID == nil || claim.OrderID != *snapshot.ClaimOrderID || claim.OrderID != *only {
			return reject("evidence_insufficient", "claim_missing")
		}
		var order model.Order
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, claim.OrderID).Error; err != nil {
			return err
		}
		if order.ChannelID != claim.ChannelID || order.MerchantID != claim.MerchantID || order.PayType != event.Provider || order.Amount.Shift(2).IntPart() != event.AmountMinor || !order.RealAmount.Equal(order.Amount) || order.DeletedAt.Valid {
			return reject("evidence_insufficient", "scope_changed")
		}
		var consumed int64
		if err := tx.Table("observer_consumptions").Where("order_id=? OR event_pk=?", order.ID, event.ID).Count(&consumed).Error; err != nil {
			return err
		}
		if order.Status == model.OrderStatusPaid || order.Status == model.OrderStatusRefund || consumed != 0 {
			return reject("duplicate_suspected", "order_settled")
		}
		if order.Status != model.OrderStatusUnpaid {
			return reject("evidence_insufficient", "scope_changed")
		}
		if event.FirstReceivedAtMs >= claim.ObserverPayExpiresAtMs || event.ObservedAtMs >= claim.ObserverPayExpiresAtMs {
			return reject("expired", "window_expired")
		}
		if event.ObservedAtMs < claim.CreatedAtMs {
			return reject("clock_error", "observed_before_order")
		}
		if event.FirstReceivedAtMs < claim.CreatedAtMs || order.ObserverPayExpiresAt == nil || order.ObserverPayExpiresAt.UnixMilli() != claim.ObserverPayExpiresAtMs {
			return reject("evidence_insufficient", "scope_changed")
		}
		var allow struct{ Enabled bool }
		if err := tx.Raw(`SELECT enabled FROM observer_synthetic_merchants WHERE environment=? AND merchant_id=? FOR SHARE`, event.Environment, order.MerchantID).Scan(&allow).Error; err != nil {
			return err
		}
		var merchant model.Merchant
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&merchant, order.MerchantID).Error; err != nil {
			return err
		}
		if !allow.Enabled || merchant.Status != 1 || merchant.DeletedAt.Valid {
			return reject("evidence_insufficient", "scope_changed")
		}
		ledgerID, err := accounting.CreditLockedOrder(tx, &order, nil, "", time.UnixMilli(now))
		if err != nil {
			return err
		}
		decision.Outcome = "matched"
		decision.Reason = "eligible"
		return persistDecision(tx, decision, ledgerID)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return MatchingDecision{}, err
	}
	return decision, nil
}

func persistDecision(tx *gorm.DB, d MatchingDecision, ledgerID int64) error {
	if err := tx.Exec(`INSERT INTO observer_match_decisions(event_pk,policy_version,outcome,reason,candidate_count,candidate_cutoff_ms,order_id,decided_at_ms) VALUES (?,?,?,?,?,?,?,?)`, d.EventPK, d.PolicyVersion, d.Outcome, d.Reason, d.CandidateCount, d.CandidateCutoffMs, d.OrderID, d.DecidedAtMs).Error; err != nil {
		return err
	}
	state := "needs_review"
	if d.Outcome == "matched" {
		state = "credited"
		if err := tx.Exec(`INSERT INTO observer_consumptions(event_pk,order_id,ledger_id,consumed_at_ms) VALUES (?,?,?,?)`, d.EventPK, d.OrderID, ledgerID, d.DecidedAtMs).Error; err != nil {
			return err
		}
	}
	return tx.Exec(`UPDATE observer_events SET processing_status=? WHERE id=?`, state, d.EventPK).Error
}

func lockMatchingAuthority(tx *gorm.DB, e eventRow, now int64) (bool, error) {
	var env struct {
		Enabled     bool
		SourceClass string
	}
	if err := tx.Raw(`SELECT enabled,source_class FROM observer_environments WHERE environment=? FOR SHARE`, e.Environment).Scan(&env).Error; err != nil {
		return false, err
	}
	var receiver struct {
		Enabled  bool
		Provider string
	}
	if err := tx.Raw(`SELECT enabled,provider FROM observer_receivers WHERE environment=? AND receiver_id=? FOR UPDATE`, e.Environment, e.ReceiverID).Scan(&receiver).Error; err != nil {
		return false, err
	}
	var device struct{ Enabled bool }
	if err := tx.Raw(`SELECT enabled FROM observer_devices WHERE environment=? AND device_id=? FOR SHARE`, e.Environment, e.DeviceID).Scan(&device).Error; err != nil {
		return false, err
	}
	var binding struct{ Active bool }
	if err := tx.Raw(`SELECT active FROM observer_bindings WHERE environment=? AND receiver_id=? AND binding_version=? AND device_id=? AND provider=? FOR SHARE`, e.Environment, e.ReceiverID, e.BindingVersion, e.DeviceID, e.Provider).Scan(&binding).Error; err != nil {
		return false, err
	}
	var credential struct {
		State                    string
		NotBeforeMs, ExpiresAtMs int64
	}
	if err := tx.Raw(`SELECT state,not_before_ms,expires_at_ms FROM observer_credentials WHERE key_id=? FOR SHARE`, e.FirstKeyID).Scan(&credential).Error; err != nil {
		return false, err
	}
	return env.Enabled && env.SourceClass == "synthetic" && receiver.Enabled && receiver.Provider == e.Provider && device.Enabled && binding.Active && credential.State == "active" && now >= credential.NotBeforeMs && now < credential.ExpiresAtMs, nil
}

func (m *Matcher) ProcessPending(ctx context.Context, limit int) error {
	if m == nil || m.environment == "" || m.environment != SyntheticEnvironment() {
		return ErrSyntheticDisabled
	}
	if limit < 1 || limit > 50 {
		return ErrInput
	}
	var ids []int64
	if err := m.db.WithContext(ctx).Raw(`SELECT id FROM observer_events WHERE environment=? AND processing_status='pending' ORDER BY id LIMIT ?`, m.environment, limit).Scan(&ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := m.ProcessEvent(ctx, id, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}
func (m *Matcher) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := m.ProcessPending(ctx, 50); err != nil {
			log.Print("observer settlement scan failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
