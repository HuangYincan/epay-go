package observerstore

import (
	"context"
	"database/sql"
	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
)

type ConfirmationOperation struct {
	ID           int64  `json:"id"`
	ActorID      int64  `json:"actor_id"`
	Paused       bool   `json:"paused"`
	ReasonCode   string `json:"reason_code"`
	OperatedAtMs int64  `json:"operated_at_ms"`
}
type ConfirmationResult struct {
	SchemaVersion int    `json:"schema_version"`
	ReceiverID    string `json:"receiver_id"`
	Paused        bool   `json:"paused"`
	OperationID   int64  `json:"operation_id"`
	FrozenEvents  int64  `json:"frozen_events"`
}

// Control changes serialize with receipt and settlement on the receiver lock.
// Resume never touches old evidence, decisions, claims or income.
func (p *PairingService) SetConfirmationPaused(ctx context.Context, receiver string, paused bool, reason string, actor, now int64) (ConfirmationResult, error) {
	result := ConfirmationResult{SchemaVersion: 1, ReceiverID: receiver, Paused: paused}
	if !observer.ValidIdentifier(receiver) || actor <= 0 || now < 0 || now > observer.MaxTime || (reason != "incident" && reason != "maintenance" && reason != "review_completed") {
		return result, ErrInput
	}
	err := p.transaction(ctx, func(tx *gorm.DB) error {
		var environment string
		found := tx.Raw(`SELECT environment FROM observer_environments WHERE environment=? FOR SHARE`, p.environment).Scan(&environment)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected != 1 {
			return ErrScope
		}
		var scoped string
		found = tx.Raw(`SELECT receiver_id FROM observer_receivers WHERE environment=? AND receiver_id=? FOR UPDATE`, p.environment, receiver).Scan(&scoped)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected != 1 {
			return ErrScope
		}
		var actors int64
		if err := tx.Raw(`SELECT count(*) FROM admins WHERE id=? AND deleted_at IS NULL`, actor).Scan(&actors).Error; err != nil {
			return err
		}
		if actors != 1 {
			return ErrScope
		}
		if err := tx.Raw(`INSERT INTO observer_confirmation_operations(environment,receiver_id,actor_id,paused,reason_code,operated_at_ms) VALUES (?,?,?,?,?,?) RETURNING id`, p.environment, receiver, actor, paused, reason, now).Scan(&result.OperationID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO observer_confirmation_controls(environment,receiver_id,paused,last_operation_id) VALUES (?,?,?,?) ON CONFLICT(environment,receiver_id) DO UPDATE SET paused=EXCLUDED.paused,last_operation_id=EXCLUDED.last_operation_id`, p.environment, receiver, paused, result.OperationID).Error; err != nil {
			return err
		}
		if !paused {
			return nil
		}
		frozen := tx.Exec(`INSERT INTO observer_match_decisions(event_pk,policy_version,outcome,reason,candidate_count,candidate_cutoff_ms,order_id,decided_at_ms)
    SELECT e.id,?,'evidence_insufficient','confirmation_paused',COALESCE(s.candidate_count,0),e.first_received_at_ms,s.candidate_order_id,?
    FROM observer_events e LEFT JOIN observer_candidate_snapshots s ON s.event_pk=e.id
    WHERE e.environment=? AND e.receiver_id=? AND e.processing_status='pending'`, SyntheticPolicy, now, p.environment, receiver)
		if frozen.Error != nil {
			return frozen.Error
		}
		result.FrozenEvents = frozen.RowsAffected
		if err := tx.Exec(`UPDATE observer_events SET processing_status='needs_review' WHERE environment=? AND receiver_id=? AND processing_status='pending'`, p.environment, receiver).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM observer_match_retries r USING observer_events e WHERE r.event_pk=e.id AND e.environment=? AND e.receiver_id=? AND e.processing_status='needs_review'`, p.environment, receiver).Error
	})
	return result, err
}

type EventTrace struct {
	EventPK            int64   `json:"event_pk"`
	EventID            string  `json:"event_id"`
	Provider           string  `json:"provider"`
	AmountMinor        int64   `json:"amount_minor"`
	ObservedAtMs       int64   `json:"observed_at_ms"`
	FirstReceivedAtMs  int64   `json:"first_received_at_ms"`
	ParserVersion      string  `json:"parser_version"`
	IdentityConfidence string  `json:"identity_confidence"`
	ProcessingStatus   string  `json:"processing_status"`
	PolicyVersion      *string `json:"policy_version"`
	Outcome            *string `json:"outcome"`
	Reason             *string `json:"reason"`
	CandidateCount     *int64  `json:"candidate_count"`
	CandidateOrderID   *int64  `json:"candidate_order_id"`
	OrderTradeNo       *string `json:"order_trade_no"`
	MerchantOrderNo    *string `json:"merchant_order_no"`
	LedgerID           *int64  `json:"ledger_id"`
	IncomeMinor        *int64  `json:"income_minor"`
	NotifyStatus       *int    `json:"notify_status"`
	NotifyCount        *int    `json:"notify_count"`
}
type TraceResult struct {
	SchemaVersion     int                     `json:"schema_version"`
	ReceiverID        string                  `json:"receiver_id"`
	Paused            bool                    `json:"paused"`
	Events            []EventTrace            `json:"events"`
	Operations        []ConfirmationOperation `json:"operations"`
	NextBeforeEventPK int64                   `json:"next_before_event_pk"`
}

func (p *PairingService) QueryEventTrace(ctx context.Context, receiver string, before int64, limit int, actor int64) (TraceResult, error) {
	result := TraceResult{SchemaVersion: 1, ReceiverID: receiver, Events: []EventTrace{}, Operations: []ConfirmationOperation{}}
	if !observer.ValidIdentifier(receiver) || before < 0 || limit < 1 || limit > 100 || actor <= 0 {
		return result, ErrInput
	}
	err := p.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var admins int64
		if err := db.Raw(`SELECT count(*) FROM admins WHERE id=? AND deleted_at IS NULL`, actor).Scan(&admins).Error; err != nil {
			return err
		}
		if admins != 1 {
			return ErrScope
		}
		var n int64
		if err := db.Raw(`SELECT count(*) FROM observer_receivers WHERE environment=? AND receiver_id=?`, p.environment, receiver).Scan(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return ErrScope
		}
		if err := db.Raw(`SELECT paused FROM observer_confirmation_controls WHERE environment=? AND receiver_id=?`, p.environment, receiver).Scan(&result.Paused).Error; err != nil {
			return err
		}
		if err := db.Raw(`SELECT e.id AS event_pk,e.event_id,e.provider,e.amount_minor,e.observed_at_ms,e.first_received_at_ms,e.parser_version,e.identity_confidence,e.processing_status,
 d.policy_version,d.outcome,d.reason,d.candidate_count,d.order_id AS candidate_order_id,o.trade_no AS order_trade_no,o.out_trade_no AS merchant_order_no,c.ledger_id,(l.amount*100)::bigint AS income_minor,o.notify_status,o.notify_count
 FROM observer_events e LEFT JOIN observer_match_decisions d ON d.event_pk=e.id
 LEFT JOIN observer_consumptions c ON c.event_pk=e.id LEFT JOIN orders o ON o.id=c.order_id LEFT JOIN balance_records l ON l.id=c.ledger_id
 WHERE e.environment=? AND e.receiver_id=? AND (?=0 OR e.id<?) ORDER BY e.id DESC LIMIT ?`, p.environment, receiver, before, before, limit).Scan(&result.Events).Error; err != nil {
			return err
		}
		if err := db.Raw(`SELECT id,actor_id,paused,reason_code,operated_at_ms FROM observer_confirmation_operations WHERE environment=? AND receiver_id=? ORDER BY id DESC LIMIT 20`, p.environment, receiver).Scan(&result.Operations).Error; err != nil {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, publicError(err)
	}
	if len(result.Events) == limit {
		result.NextBeforeEventPK = result.Events[len(result.Events)-1].EventPK
	}
	return result, nil
}
