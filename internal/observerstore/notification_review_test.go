package observerstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerhttp"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// All inputs are synthetic. The notification enum exercises admission, not App compatibility.
type notificationFixture struct {
	db         *gorm.DB
	svc        *observerstore.PairingService
	enrollment observer.Enrollment
	actor      int64
	merchant   model.Merchant
	channel    model.Channel
}

func newNotificationFixture(t *testing.T, provider, origin string) *notificationFixture {
	t.Helper()
	db, _, actor := pairingDB(t)
	exec(t, db, `UPDATE observer_environments SET enabled=true WHERE environment='prod'`)
	receiver := "prod-receiver"
	if provider == "alipay" {
		receiver = "notification-receiver-ali"
		exec(t, db, `INSERT INTO observer_receivers(environment,receiver_id,provider,account_ref,enabled) VALUES ('prod',?,'alipay','synthetic-ali-account',true)`, receiver)
	} else {
		exec(t, db, `UPDATE observer_receivers SET enabled=true WHERE environment='prod'`)
	}
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := observerstore.NewPairingService(db, "prod", "notification", origin, cipher)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := svc.CreateGrant(context.Background(), receiver, "replace", actor, pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	e, err := svc.Redeem(context.Background(), token, request(), pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(context.Background(), token, request(), pairingNow); err != nil {
		t.Fatal(err)
	}
	f := &notificationFixture{db: db, svc: svc, enrollment: e, actor: actor,
		merchant: model.Merchant{Username: "review-merchant", Password: "unused", ApiKey: "test-review-key", Balance: decimal.NewFromInt(3)},
		channel:  model.Channel{Name: "review-channel", Plugin: "epay", Status: 1}}
	if err := db.Create(&f.merchant).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&f.channel).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *notificationFixture) order(t *testing.T, no string, created int64, expires *time.Time, status int8) *model.Order {
	t.Helper()
	o := &model.Order{BaseModel: model.BaseModel{CreatedAt: time.UnixMilli(created)}, TradeNo: no, OutTradeNo: no,
		MerchantID: f.merchant.ID, ChannelID: f.channel.ID, PayType: f.enrollment.Provider,
		Amount: decimal.NewFromInt(10), RealAmount: decimal.NewFromInt(10), Status: status, CheckoutExpiresAt: expires}
	if err := f.db.Create(o).Error; err != nil {
		t.Fatal(err)
	}
	return o
}

func (f *notificationFixture) link(t *testing.T) {
	t.Helper()
	// Legacy orders precede this test-only mapping. Production alias/claim behavior is unchanged.
	exec(t, f.db, `INSERT INTO observer_channel_aliases VALUES (?,'prod',?,?)`, f.channel.ID, f.enrollment.ReceiverID, f.enrollment.Provider)
}

func (f *notificationFixture) body(t *testing.T, event int, observed int64) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"schema_version": 1, "event_id": fmt.Sprintf("%08x-1111-4111-8111-111111111111", event),
		"device_id": f.enrollment.DeviceID, "receiver_id": f.enrollment.ReceiverID, "binding_version": f.enrollment.BindingVersion,
		"provider": f.enrollment.Provider, "source_class": "notification", "amount_minor": 1000, "currency": "CNY",
		"observed_at_ms": observed, "parser_version": "unverified-test-rule", "identity_confidence": "distinct", "provider_transaction_id": "synthetic-platform-reference"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *notificationFixture) receive(t *testing.T, event, request int, observed, received int64) observerstore.ReceiveResult {
	t.Helper()
	b := f.body(t, event, observed)
	return observerstore.NewReceiver(f.svc).Receive(context.Background(), receiveRequest(t, f.enrollment, b, reqID(request), received), b, received)
}

func (f *notificationFixture) trace(t *testing.T) observerstore.EventTrace {
	t.Helper()
	r, err := f.svc.QueryEventTrace(context.Background(), f.enrollment.ReceiverID, 0, 100, f.actor)
	if err != nil || len(r.Events) == 0 {
		t.Fatal("trace", r, err)
	}
	b, _ := json.Marshal(r)
	for _, field := range []string{"device_id", "key_id", "signature", "key_ciphertext", "body_base64", "provider_transaction_id"} {
		if bytes.Contains(b, []byte(field)) {
			t.Fatal("sensitive trace field", field)
		}
	}
	return r.Events[0]
}

func (f *notificationFixture) noSettlement(t *testing.T) {
	t.Helper()
	for _, table := range []string{"observer_consumptions", "balance_records", "observer_amount_claims", "observer_match_retries"} {
		if count(t, f.db, table) != 0 {
			t.Fatal("unexpected side effect", table)
		}
	}
	var merchant model.Merchant
	f.db.First(&merchant, f.merchant.ID)
	if !merchant.Balance.Equal(f.merchant.Balance) {
		t.Fatal("merchant credited")
	}
	var started int64
	f.db.Model(&model.Order{}).Where("notify_status <> 0 OR notify_count <> 0 OR next_notify_at IS NOT NULL").Count(&started)
	if started != 0 {
		t.Fatal("notification callback scheduled")
	}
}

func TestNotificationReviewReasons(t *testing.T) {
	deadline := time.UnixMilli(pairingNow + 600000)
	tests := []struct {
		name, outcome, reason       string
		orders                      int
		created, observed, received int64
		expires                     *time.Time
		status                      int8
		paused                      bool
	}{
		{"zero", "unmatched", "no_candidate", 0, pairingNow, pairingNow + 1000, pairingNow + 1000, &deadline, 0, false},
		{"unique", "evidence_insufficient", "confirmation_not_enabled", 1, pairingNow, pairingNow + 1000, pairingNow + 1000, &deadline, 0, false},
		{"same-amount", "ambiguous", "multiple_candidates", 2, pairingNow, pairingNow + 1000, pairingNow + 1000, &deadline, 0, false},
		{"no-expiry", "evidence_insufficient", "window_unconfigured", 1, pairingNow, pairingNow + 1000, pairingNow + 1000, nil, 0, false},
		{"expired", "expired", "window_expired", 1, pairingNow, pairingNow + 600000, pairingNow + 600000, &deadline, 0, false},
		{"before-order", "clock_error", "observed_before_order", 1, pairingNow + 1000, pairingNow, pairingNow + 1000, &deadline, 0, false},
		{"future-clock", "clock_error", "clock_skew", 1, pairingNow, pairingNow + 3001, pairingNow + 1000, &deadline, 0, false},
		{"stale", "stale_observation", "stale_observation", 1, pairingNow, pairingNow, pairingNow + 120001, &deadline, 0, false},
		{"paid", "duplicate_suspected", "order_settled", 1, pairingNow, pairingNow + 1000, pairingNow + 1000, &deadline, 1, false},
		{"refund", "duplicate_suspected", "order_settled", 1, pairingNow, pairingNow + 1000, pairingNow + 1000, &deadline, 2, false},
		{"paused", "evidence_insufficient", "confirmation_paused", 1, pairingNow, pairingNow + 1000, pairingNow + 1000, &deadline, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNotificationFixture(t, "alipay", "https://observer.example")
			for i := 0; i < tt.orders; i++ {
				f.order(t, fmt.Sprintf("legacy-%d", i), tt.created, tt.expires, tt.status)
			}
			f.link(t)
			if tt.paused {
				if _, err := f.svc.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, true, "incident", f.actor, pairingNow); err != nil {
					t.Fatal(err)
				}
			}
			r := f.receive(t, 1, 1, tt.observed, tt.received)
			if r.Status != 202 || r.Receipt == nil || r.Receipt.ProcessingStatus != "needs_review" {
				t.Fatalf("ACK=%+v", r)
			}
			tr := f.trace(t)
			if tr.PolicyVersion == nil || *tr.PolicyVersion != observerstore.NotificationReviewPolicy || tr.Outcome == nil || *tr.Outcome != tt.outcome || tr.Reason == nil || *tr.Reason != tt.reason || tr.CandidateCount == nil || *tr.CandidateCount != int64(tt.orders) {
				t.Fatalf("trace=%+v", tr)
			}
			if tr.OrderTradeNo != nil || tr.LedgerID != nil {
				t.Fatal("candidate reported as consumed")
			}
			if tt.orders == 1 && (tr.CandidateOrderTradeNo == nil || *tr.CandidateOrderTradeNo != "legacy-0") {
				t.Fatal("missing candidate")
			}
			f.noSettlement(t)
		})
	}
}

func TestNotificationReviewTLSAndConcurrentReplay(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	f := newNotificationFixture(t, "wxpay", "https://"+server.Listener.Addr().String())
	server.Config.Handler = observerhttp.Router(f.svc)
	server.StartTLS()
	defer server.Close()
	now := time.Now().UnixMilli()
	expires := time.UnixMilli(now + 60000)
	f.order(t, "legacy-tls", now-1000, &expires, 0)
	f.link(t)
	exec(t, f.db, `UPDATE observer_credentials SET expires_at_ms=? WHERE key_id=?`, now+60000, f.enrollment.KeyID)
	body := f.body(t, 1, now)
	r := receiveRequest(t, f.enrollment, body, reqID(1), now)
	r.URL, _ = url.Parse(server.URL + observer.Path)
	r.Host = r.URL.Host
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var ack observer.Receipt
	err = json.NewDecoder(response.Body).Decode(&ack)
	response.Body.Close()
	if err != nil || response.StatusCode != 202 || ack.ProcessingStatus != "needs_review" {
		t.Fatal("TLS receipt", response.StatusCode, ack, err)
	}
	before := f.trace(t)
	results := make(chan observerstore.ReceiveResult, 8)
	for i := 2; i < 10; i++ {
		req := receiveRequest(t, f.enrollment, body, reqID(i), now+10)
		go func() { results <- observerstore.NewReceiver(f.svc).Receive(context.Background(), req, body, now+10) }()
	}
	for i := 0; i < 8; i++ {
		got := <-results
		if got.Status != 200 || got.Receipt == nil || got.Receipt.ProcessingStatus != "needs_review" {
			t.Fatal("duplicate ACK", got)
		}
	}
	if count(t, f.db, "observer_events") != 1 || count(t, f.db, "observer_candidate_snapshots") != 1 || count(t, f.db, "observer_match_decisions") != 1 {
		t.Fatal("replay duplicated history")
	}
	if _, err := f.svc.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, true, "incident", f.actor, now+20); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, false, "review_completed", f.actor, now+30); err != nil {
		t.Fatal(err)
	}
	after := f.trace(t)
	if *before.Reason != *after.Reason || before.CandidateOrderID == nil || after.CandidateOrderID == nil || *before.CandidateOrderID != *after.CandidateOrderID {
		t.Fatal("history reinterpreted")
	}
	f.noSettlement(t)
}

func TestNotificationReviewRollbackAndHistory(t *testing.T) {
	f := newNotificationFixture(t, "alipay", "https://observer.example")
	exec(t, f.db, `CREATE FUNCTION fail_review() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test_failure'; END $$; CREATE TRIGGER fail_review BEFORE INSERT ON observer_match_decisions FOR EACH ROW EXECUTE FUNCTION fail_review()`)
	if r := f.receive(t, 1, 1, pairingNow, pairingNow); r.Status != 503 {
		t.Fatal("failed transaction ACK", r)
	}
	for _, table := range []string{"observer_requests", "observer_events", "observer_candidate_snapshots", "observer_match_decisions", "observer_device_budgets"} {
		if count(t, f.db, table) != 0 {
			t.Fatal("partial commit", table)
		}
	}
	exec(t, f.db, `DROP TRIGGER fail_review ON observer_match_decisions`)
	if r := f.receive(t, 1, 1, pairingNow, pairingNow); r.Status != 202 {
		t.Fatal("retry after rollback", r)
	}
	// A future same-millisecond order/mapping cannot upgrade the first zero-candidate event.
	f.order(t, "future", pairingNow, nil, 0)
	f.link(t)
	if r := f.receive(t, 1, 2, pairingNow, pairingNow+1); r.Status != 200 {
		t.Fatal(r)
	}
	tr := f.trace(t)
	if tr.Reason == nil || *tr.Reason != "no_candidate" || tr.CandidateCount == nil || *tr.CandidateCount != 0 {
		t.Fatal("future order reinterpreted", tr)
	}
	if r := f.receive(t, 2, 3, pairingNow+2, pairingNow+2); r.Status != 202 {
		t.Fatal(r)
	}
	tr = f.trace(t)
	if tr.Reason == nil || *tr.Reason != "earlier_evidence" {
		t.Fatal("historical amount ignored", tr)
	}
	rejected(t, f.db, `UPDATE observer_match_decisions SET reason='confirmation_not_enabled'`)
	rejected(t, f.db, `UPDATE observer_events SET processing_status='pending'`)
	f.noSettlement(t)
}

func TestNotificationReviewDatabaseCannotAuthorizeCredit(t *testing.T) {
	f := newNotificationFixture(t, "wxpay", "https://observer.example")
	// The storage primitive deliberately omits receiver admission, leaving no decision.
	body := f.body(t, 1, pairingNow)
	request := receiveRequest(t, f.enrollment, body, reqID(1), pairingNow)
	// Obtain a separately verified request using the test-only enrollment key.
	key, err := base64.RawURLEncoding.DecodeString(f.enrollment.KeyBase64URL)
	if err != nil {
		t.Fatal(err)
	}
	v, err := observer.VerifyRequest(request, body, f.enrollment.KeyID, key, pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	result, err := observerstore.RecordEvidence(context.Background(), f.db, "prod", f.enrollment.KeyID, v, pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	o := f.order(t, "review-only", pairingNow, nil, 0)
	err = f.db.Exec(`INSERT INTO observer_match_decisions(event_pk,policy_version,outcome,reason,candidate_count,candidate_cutoff_ms,order_id,decided_at_ms) VALUES (?,'notification-review-v1','matched','eligible',1,?,?,?)`, result.EventPK, pairingNow, o.ID, pairingNow).Error
	if err == nil || !strings.Contains(err.Error(), "observer_notification_review_no_credit") {
		t.Fatal("review credit was not rejected by the database policy constraint", err)
	}
	err = f.db.Exec(`INSERT INTO observer_match_decisions(event_pk,policy_version,outcome,reason,candidate_count,candidate_cutoff_ms,decided_at_ms) VALUES (?,'synthetic-exclusive-v1','unmatched','no_candidate',0,?,?)`, result.EventPK, pairingNow, pairingNow).Error
	if err == nil || !strings.Contains(err.Error(), "observer_decision_source") {
		t.Fatal("notification source accepted a synthetic decision", err)
	}
	if count(t, f.db, "observer_match_decisions") != 0 {
		t.Fatal("invalid decision committed")
	}
	// Pausing old pending evidence must use the notification policy too, without
	// backfilling a candidate snapshot or turning a subsequent resume into payment.
	if _, err := f.svc.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, true, "incident", f.actor, pairingNow); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, false, "review_completed", f.actor, pairingNow+1); err != nil {
		t.Fatal(err)
	}
	tr := f.trace(t)
	if tr.PolicyVersion == nil || *tr.PolicyVersion != observerstore.NotificationReviewPolicy || tr.Reason == nil || *tr.Reason != "confirmation_paused" || tr.ProcessingStatus != "needs_review" || count(t, f.db, "observer_candidate_snapshots") != 0 {
		t.Fatal("legacy notification pause reinterpreted evidence", tr)
	}
	f.noSettlement(t)
}

func TestNotificationReviewUpgradePreservesPendingHistory(t *testing.T) {
	db := testDB(t)
	exec(t, db, `CREATE TABLE observer_schema_versions (version integer PRIMARY KEY, checksum text NOT NULL)`)
	// Build the actual deployed predecessor without editing its immutable scripts.
	for i, name := range []string{"001_identity.sql", "002_pairing.sql", "003_receiving.sql", "004_amount_claims.sql", "005_matching.sql", "006_confirmation_control.sql"} {
		script, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, db, string(script))
		hash := sha256.Sum256(script)
		exec(t, db, `INSERT INTO observer_schema_versions VALUES (?,?)`, i+1, hex.EncodeToString(hash[:]))
	}
	seedIdentityRows(t, db)
	result, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", verified(t, nil), pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := observerstore.Migrate(context.Background(), db); err != nil {
			t.Fatal("upgrade", err)
		}
	}
	var state string
	if err := db.Raw(`SELECT processing_status FROM observer_events WHERE id=?`, result.EventPK).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state != "pending" || count(t, db, "observer_schema_versions") != 7 || count(t, db, "observer_events") != 1 || count(t, db, "observer_match_decisions") != 0 || count(t, db, "observer_consumptions") != 0 {
		t.Fatal("upgrade reinterpreted history")
	}
}
