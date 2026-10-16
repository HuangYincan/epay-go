package observerstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type EvidenceResult struct {
	EventPK int64
	Disposition string // stored, duplicate, conflict; never an HTTP ACK or credited.
}

// RecordEvidence is a storage primitive, not a receiver. The future receiving
// transaction MUST also enforce current authorization/revocation and persist the
// request replay record. A valid signature alone does not supply those guarantees.
func RecordEvidence(ctx context.Context, db *gorm.DB, environment, keyID string, verified *observer.VerifiedRequest, receivedAt int64) (EvidenceResult, error) {
	if verified == nil || receivedAt < 0 || receivedAt > observer.MaxTime { return EvidenceResult{}, ErrInput }
	body := verified.BodyBytes()
	event, err := observer.ParseEvent(body)
	if err != nil { return EvidenceResult{}, ErrInput }
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	var result EvidenceResult
	err = db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		// Scope is immutable metadata only. Active flags/expiry are not checked
		// here; callers must not expose this primitive as an accepting endpoint.
		var scopeCount int64
		if err := tx.Raw(`SELECT count(*) FROM observer_credentials WHERE key_id = ? AND environment = ? AND device_id = ? AND receiver_id = ? AND binding_version = ? AND provider = ? AND source_class = ?`, keyID, environment, event.DeviceID, event.ReceiverID, event.BindingVersion, event.Provider, event.SourceClass).Scan(&scopeCount).Error; err != nil { return err }
		if scopeCount != 1 { return ErrScope }
		var transactionID any
		if event.ProviderTransactionID != "" { transactionID = event.ProviderTransactionID }
		inserted := tx.Exec(`INSERT INTO observer_events(environment, device_id, event_id, receiver_id, binding_version, provider, amount_minor, currency, observed_at_ms, parser_version, source_class, identity_confidence, provider_transaction_id, body, body_sha256, first_received_at_ms)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (environment, device_id, event_id) DO NOTHING`, environment, event.DeviceID, event.EventID, event.ReceiverID, event.BindingVersion, event.Provider, event.AmountMinor, event.Currency, event.ObservedAt, event.ParserVersion, event.SourceClass, event.IdentityConfidence, transactionID, body, hash, receivedAt)
		if inserted.Error != nil { return inserted.Error }
		var stored struct { ID int64; BodySHA256 string; FirstReceivedAtMs int64 }
		// INSERT's unique-conflict wait plus this new READ COMMITTED statement
		// observes the winner, including across concurrent connections.
		if err := tx.Raw(`SELECT id, body_sha256, first_received_at_ms FROM observer_events WHERE environment = ? AND device_id = ? AND event_id = ?`, environment, event.DeviceID, event.EventID).Scan(&stored).Error; err != nil { return err }
		if stored.ID == 0 { return ErrStorage }
		result.EventPK = stored.ID
		if stored.BodySHA256 != hash {
			if err := tx.Exec(`INSERT INTO observer_event_conflicts(event_pk, environment, device_id, key_id, request_id, incoming_body_sha256, first_seen_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (event_pk, incoming_body_sha256) DO NOTHING`, stored.ID, environment, event.DeviceID, keyID, verified.RequestID, hash, receivedAt).Error; err != nil { return err }
			result.Disposition = "conflict"
			return nil
		}
		result.Disposition = "duplicate"
		if inserted.RowsAffected == 1 { result.Disposition = "stored" }
		return nil
	})
	if err != nil {
		if err == ErrScope { return EvidenceResult{}, ErrScope }
		return EvidenceResult{}, ErrStorage
	}
	return result, nil
}
