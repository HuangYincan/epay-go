package observerstore

import (
	"gorm.io/gorm"
	"time"
)

type eventRow struct {
	ID                                                                                                            int64
	Environment, ReceiverID, DeviceID, Provider, SourceClass, IdentityConfidence, ParserVersion, ProcessingStatus string
	BindingVersion                                                                                                int
	AmountMinor, ObservedAtMs, FirstReceivedAtMs                                                                  int64
	FirstKeyID, FirstRequestID                                                                                    string
	ProviderTransactionID                                                                                         *string
}
type candidateSnapshot struct {
	EventPK           int64
	ClaimOrderID      *int64
	CandidateCount    int64
	CandidateOrderID  *int64
	CandidateCutoffMs int64
}

func currentCandidates(tx *gorm.DB, e eventRow) (int64, *int64, error) {
	var row struct {
		Count  int64
		OnlyID *int64
	}
	err := tx.Raw(`SELECT count(*) AS count, CASE WHEN count(*)=1 THEN min(o.id) END AS only_id
  FROM orders o JOIN observer_channel_aliases a ON a.channel_id=o.channel_id AND a.provider=o.pay_type
  WHERE a.environment=? AND a.receiver_id=? AND a.provider=? AND o.amount*100=? AND o.created_at<=?`,
		e.Environment, e.ReceiverID, e.Provider, e.AmountMinor, time.UnixMilli(e.FirstReceivedAtMs)).Scan(&row).Error
	return row.Count, row.OnlyID, err
}

// Called only on first authenticated receipt while the receiver lock is held.
// A later order in the very same millisecond must not become a candidate.
func freezeCandidates(tx *gorm.DB, eventPK int64) error {
	var e eventRow
	if err := tx.Raw(`SELECT * FROM observer_events WHERE id=?`, eventPK).Scan(&e).Error; err != nil {
		return err
	}
	count, id, err := currentCandidates(tx, e)
	if err != nil {
		return err
	}
	var claim struct{ OrderID *int64 }
	if err := tx.Raw(`SELECT order_id FROM observer_amount_claims WHERE environment=? AND receiver_id=? AND provider=? AND amount_minor=?`, e.Environment, e.ReceiverID, e.Provider, e.AmountMinor).Scan(&claim).Error; err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO observer_candidate_snapshots(event_pk,claim_order_id,candidate_count,candidate_order_id,candidate_cutoff_ms) VALUES (?,?,?,?,?)`, e.ID, claim.OrderID, count, id, e.FirstReceivedAtMs).Error; err != nil {
		return err
	}
	var paused bool
	if err := tx.Raw(`SELECT paused FROM observer_confirmation_controls WHERE environment=? AND receiver_id=?`, e.Environment, e.ReceiverID).Scan(&paused).Error; err != nil {
		return err
	}
	if e.SourceClass == "notification" {
		return reviewNotification(tx, e, count, id, paused)
	}
	if paused {
		return persistDecision(tx, MatchingDecision{EventPK: e.ID, PolicyVersion: SyntheticPolicy, Outcome: "evidence_insufficient", Reason: "confirmation_paused", CandidateCount: count, CandidateCutoffMs: e.FirstReceivedAtMs, OrderID: id, DecidedAtMs: e.FirstReceivedAtMs}, 0)
	}
	return nil
}
