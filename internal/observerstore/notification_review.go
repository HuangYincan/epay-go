package observerstore

import (
	"time"

	"github.com/example/epay-go/internal/model"
	"gorm.io/gorm"
)

const NotificationReviewPolicy = "notification-review-v1"

// Runs only on first authenticated receipt, under the receiver lock and inside
// its transaction. A candidate explains a review; it never authorizes settlement.
func reviewNotification(tx *gorm.DB, e eventRow, count int64, orderID *int64, paused bool) error {
	d := MatchingDecision{EventPK: e.ID, PolicyVersion: NotificationReviewPolicy,
		CandidateCount: count, OrderID: orderID, CandidateCutoffMs: e.FirstReceivedAtMs, DecidedAtMs: e.FirstReceivedAtMs}
	review := func(outcome, reason string) error {
		d.Outcome, d.Reason = outcome, reason
		return persistDecision(tx, d, 0)
	}
	if paused {
		return review("evidence_insufficient", "confirmation_paused")
	}
	if e.FirstReceivedAtMs-e.ObservedAtMs < -2000 {
		return review("clock_error", "clock_skew")
	}
	if e.FirstReceivedAtMs-e.ObservedAtMs > 120000 {
		return review("stale_observation", "stale_observation")
	}
	var prior int64
	if err := tx.Raw(`SELECT count(*) FROM observer_events WHERE environment=? AND receiver_id=? AND provider=? AND amount_minor=? AND id<?`,
		e.Environment, e.ReceiverID, e.Provider, e.AmountMinor, e.ID).Scan(&prior).Error; err != nil {
		return err
	}
	if prior != 0 {
		return review("duplicate_suspected", "earlier_evidence")
	}
	if count == 0 {
		return review("unmatched", "no_candidate")
	}
	if count > 1 {
		return review("ambiguous", "multiple_candidates")
	}
	var order struct {
		Status                                  int
		DeletedAt                               *time.Time
		CreatedAt                               time.Time
		ObserverPayExpiresAt, CheckoutExpiresAt *time.Time
	}
	found := tx.Raw(`SELECT status,deleted_at,created_at,observer_pay_expires_at,checkout_expires_at FROM orders WHERE id=?`, orderID).Scan(&order)
	if found.Error != nil {
		return found.Error
	}
	if found.RowsAffected != 1 || order.DeletedAt != nil {
		return review("evidence_insufficient", "scope_changed")
	}
	if order.Status == model.OrderStatusPaid || order.Status == model.OrderStatusRefund {
		return review("duplicate_suspected", "order_settled")
	}
	if order.Status != model.OrderStatusUnpaid {
		return review("evidence_insufficient", "scope_changed")
	}
	if e.ObservedAtMs < order.CreatedAt.UnixMilli() {
		return review("clock_error", "observed_before_order")
	}
	expires := order.CheckoutExpiresAt
	if order.ObserverPayExpiresAt != nil && (expires == nil || order.ObserverPayExpiresAt.Before(*expires)) {
		expires = order.ObserverPayExpiresAt
	}
	if expires == nil {
		return review("evidence_insufficient", "window_unconfigured")
	}
	if e.FirstReceivedAtMs >= expires.UnixMilli() || e.ObservedAtMs >= expires.UnixMilli() {
		return review("expired", "window_expired")
	}
	return review("evidence_insufficient", "confirmation_not_enabled")
}
