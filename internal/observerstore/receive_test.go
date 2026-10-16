package observerstore_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example/epay-go/internal/observerhttp"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
)

const receiveEventID = "11111111-1111-4111-8111-111111111111"

func receiveFixture(t *testing.T) (*gorm.DB, *observerstore.PairingService, observer.Enrollment) {
	t.Helper()
	db, svc, actor := pairingDB(t)
	token := grant(t, svc, actor, "replace", pairingNow)
	enrollment, err := svc.Redeem(context.Background(), token, request(), pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(context.Background(), token, request(), pairingNow); err != nil {
		t.Fatal(err)
	}
	return db, svc, enrollment
}
func receiveBody(t *testing.T, e observer.Enrollment, eventID string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"schema_version": 1, "event_id": eventID, "device_id": e.DeviceID, "receiver_id": e.ReceiverID, "binding_version": e.BindingVersion, "provider": e.Provider, "amount_minor": 1000, "currency": "CNY", "observed_at_ms": pairingNow, "parser_version": "fixture-wx-v1", "source_class": e.SourceClass, "identity_confidence": "uncertain"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func receiveRequest(t *testing.T, e observer.Enrollment, body []byte, id string, sent int64) *http.Request {
	t.Helper()
	r, err := http.NewRequest("POST", e.Origin+observer.Path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.RawURLEncoding.DecodeString(e.KeyBase64URL)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	hash := sha256.Sum256(body)
	time := strconv.FormatInt(sent, 10)
	input := strings.Join([]string{"epay-observer-v1", "POST", observer.Path, e.KeyID, time, id, hex.EncodeToString(hash[:])}, "\n")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(input))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Observer-Version", "1")
	r.Header.Set("X-Observer-Key-Id", e.KeyID)
	r.Header.Set("X-Observer-Sent-At", time)
	r.Header.Set("X-Observer-Request-Id", id)
	r.Header.Set("X-Observer-Signature", hex.EncodeToString(mac.Sum(nil)))
	return r
}
func reqID(n int) string { return fmt.Sprintf("%08x-2222-4222-8222-222222222222", n) }

func TestReceiveConcurrentDuplicatesAndReplaySemantics(t *testing.T) {
	ctx := context.Background()
	db, svc, enrollment := receiveFixture(t)
	receiver := observerstore.NewReceiver(svc)
	body := receiveBody(t, enrollment, receiveEventID)
	results := make(chan observerstore.ReceiveResult, 12)
	for i := 0; i < 12; i++ {
		r := receiveRequest(t, enrollment, body, reqID(i), pairingNow)
		go func() { results <- receiver.Receive(ctx, r, body, pairingNow) }()
	}
	accepted := 0
	var firstReceipt *observer.Receipt
	for i := 0; i < 12; i++ {
		got := <-results
		if got.Status == 202 {
			accepted++
			firstReceipt = got.Receipt
		} else if got.Status != 200 {
			t.Fatalf("receive: %+v", got)
		}
		if got.Receipt == nil || got.Receipt.EventID != receiveEventID || got.Receipt.ReceivedAtMs != pairingNow {
			t.Fatal("invalid durable receipt")
		}
	}
	if accepted != 1 || count(t, db, "observer_events") != 1 || count(t, db, "observer_requests") != 12 {
		t.Fatal("duplicate consumption or replay records missing")
	}
	golden, err := os.ReadFile("../../pkg/observer/testdata/receipts.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct{ Acks []struct{ ID, Body string } }
	if err := json.Unmarshal(golden, &vectors); err != nil {
		t.Fatal(err)
	}
	var expected, actual map[string]any
	if err := json.Unmarshal([]byte(vectors.Acks[0].Body), &expected); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(firstReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatal("durable ACK differs from cross-language contract")
	}
	replay := receiver.Receive(ctx, receiveRequest(t, enrollment, body, reqID(0), pairingNow), body, pairingNow+1)
	if replay.Status != 200 || replay.Receipt.ReceivedAtMs != pairingNow {
		t.Fatal("same envelope not duplicate")
	}
	conflict := receiver.Receive(ctx, receiveRequest(t, enrollment, body, reqID(0), pairingNow+1), body, pairingNow+1)
	if conflict.Status != 409 || conflict.Code != "request_conflict" {
		t.Fatal("request ID reused with different envelope")
	}
	changed := append(append([]byte(nil), body...), ' ')
	r := receiveRequest(t, enrollment, changed, reqID(99), pairingNow)
	if got := receiver.Receive(ctx, r, changed, pairingNow); got.Status != 409 || got.Code != "event_conflict" {
		t.Fatal("event conflict not preserved")
	}
	if got := receiver.Receive(ctx, r, changed, pairingNow); got.Status != 409 || got.Code != "event_conflict" {
		t.Fatal("conflict replay lost original decision")
	}
	if count(t, db, "observer_events") != 1 || count(t, db, "observer_event_conflicts") != 1 || count(t, db, "observer_requests") != 13 {
		t.Fatal("conflict overwrote evidence")
	}
	var frozen []byte
	db.Raw(`SELECT body FROM observer_events`).Row().Scan(&frozen)
	if !bytes.Equal(body, frozen) {
		t.Fatal("first bytes changed")
	}
}
func TestReceiveAuthorizationSignatureAndRevokedReplay(t *testing.T) {
	ctx := context.Background()
	db, svc, enrollment := receiveFixture(t)
	receiver := observerstore.NewReceiver(svc)
	body := receiveBody(t, enrollment, receiveEventID)
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		body   []byte
		code   string
		status int
	}{
		{"signature", func(r *http.Request) { r.Header.Set("X-Observer-Signature", strings.Repeat("0", 64)) }, body, "invalid_signature", 401},
		{"clock", func(r *http.Request) { r.Header.Set("X-Observer-Sent-At", strconv.FormatInt(pairingNow-300001, 10)) }, body, "clock_skew", 401},
		{"unknown-key", func(r *http.Request) { r.Header.Set("X-Observer-Key-Id", "missing") }, body, "unknown_key", 401},
		{"tampered-body", func(r *http.Request) {}, append(append([]byte(nil), body...), ' '), "invalid_signature", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := receiveRequest(t, enrollment, body, reqID(1), pairingNow)
			tc.mutate(r)
			got := receiver.Receive(ctx, r, tc.body, pairingNow)
			if got.Status != tc.status || got.Code != tc.code {
				t.Fatalf("%+v", got)
			}
		})
	}
	other := bytes.Replace(body, []byte(enrollment.DeviceID), []byte("wrong-device"), 1)
	if got := receiver.Receive(ctx, receiveRequest(t, enrollment, other, reqID(2), pairingNow), other, pairingNow); got.Status != 403 || got.Code != "scope_denied" {
		t.Fatal("device scope accepted")
	}
	if count(t, db, "observer_events") != 0 || count(t, db, "observer_requests") != 0 {
		t.Fatal("rejected authentication persisted evidence")
	}
	r := receiveRequest(t, enrollment, body, reqID(3), pairingNow)
	if got := receiver.Receive(ctx, r, body, pairingNow); got.Status != 202 {
		t.Fatal(got)
	}
	if err := svc.RevokeBinding(ctx, enrollment.ReceiverID, enrollment.BindingVersion, pairingNow+1); err != nil {
		t.Fatal(err)
	}
	if got := receiver.Receive(ctx, r, body, pairingNow+1); got.Status != 403 {
		t.Fatal("replay bypassed revocation")
	}
	if count(t, db, "observer_requests") != 1 {
		t.Fatal("revoked replay changed audit")
	}
}
func TestReceiveCommitFailureAndServiceRecovery(t *testing.T) {
	ctx := context.Background()
	db, svc, enrollment := receiveFixture(t)
	body := receiveBody(t, enrollment, receiveEventID)
	r := receiveRequest(t, enrollment, body, reqID(1), pairingNow)
	exec(t, db, `CREATE FUNCTION reject_receive_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test receive rollback'; END $$`)
	exec(t, db, `CREATE CONSTRAINT TRIGGER receive_commit_failure AFTER INSERT ON observer_requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_receive_commit()`)
	got := observerstore.NewReceiver(svc).Receive(ctx, r, body, pairingNow)
	if got.Status != 503 || got.Receipt != nil {
		t.Fatal("ACK before durable commit")
	}
	for _, table := range []string{"observer_events", "observer_requests", "observer_device_budgets"} {
		if count(t, db, table) != 0 {
			t.Fatal("partial receive transaction", table)
		}
	}
	exec(t, db, `DROP TRIGGER receive_commit_failure ON observer_requests`)
	if got := observerstore.NewReceiver(pairingService(t, db)).Receive(ctx, r, body, pairingNow); got.Status != 202 {
		t.Fatal(got)
	}
	if got := observerstore.NewReceiver(pairingService(t, db)).Receive(ctx, receiveRequest(t, enrollment, body, reqID(2), pairingNow+1), body, pairingNow+1); got.Status != 200 {
		t.Fatal("lost ACK created another event")
	}
	if count(t, db, "observer_events") != 1 {
		t.Fatal("event lost across service restart")
	}
}
func TestReceivePersistentDeviceBudgetAndClockRollback(t *testing.T) {
	ctx := context.Background()
	db, svc, enrollment := receiveFixture(t)
	receiver := observerstore.NewReceiver(svc)
	body := receiveBody(t, enrollment, receiveEventID)
	for i := 0; i < 20; i++ {
		got := receiver.Receive(ctx, receiveRequest(t, enrollment, body, reqID(i), pairingNow+1000), body, pairingNow+1000)
		if got.Status != 200 && got.Status != 202 {
			t.Fatal(i, got)
		}
	}
	receiver = observerstore.NewReceiver(pairingService(t, db))
	for _, now := range []int64{pairingNow + 1000, pairingNow + 999} {
		got := receiver.Receive(ctx, receiveRequest(t, enrollment, body, reqID(21), now), body, now)
		if got.Status != 429 || got.RetryAfterSeconds != 1 {
			t.Fatal("budget reset or clock rollback added tokens", got)
		}
	}
	if count(t, db, "observer_requests") != 20 {
		t.Fatal("limited request registered")
	}
	if got := receiver.Receive(ctx, receiveRequest(t, enrollment, body, reqID(22), pairingNow+2000), body, pairingNow+2000); got.Status != 200 {
		t.Fatal("budget did not refill", got)
	}
}

func TestReceiveRotationKeepsEventIdentityAndDeviceBudget(t *testing.T) {
	ctx := context.Background()
	db, svc, old := receiveFixture(t)
	receiver := observerstore.NewReceiver(svc)
	body := receiveBody(t, old, receiveEventID)
	if got := receiver.Receive(ctx, receiveRequest(t, old, body, reqID(1), pairingNow), body, pairingNow); got.Status != 202 {
		t.Fatal(got)
	}
	var actor int64
	if err := db.Raw(`SELECT id FROM admins LIMIT 1`).Scan(&actor).Error; err != nil {
		t.Fatal(err)
	}
	token := grant(t, svc, actor, "rotate", pairingNow+10)
	req := request()
	req.EnrollmentID = "22222222-2222-4222-8222-222222222222"
	current, err := svc.Redeem(ctx, token, req, pairingNow+10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(ctx, token, req, pairingNow+10); err != nil {
		t.Fatal(err)
	}
	if got := receiver.Receive(ctx, receiveRequest(t, old, body, reqID(2), pairingNow+10), body, pairingNow+10); got.Code != "revoked" || got.Status != 403 {
		t.Fatal("old key accepted after rotation", got)
	}
	if got := receiver.Receive(ctx, receiveRequest(t, current, body, reqID(3), pairingNow+10), body, pairingNow+10); got.Status != 200 {
		t.Fatal("new key could not retry immutable body", got)
	}
	var first string
	db.Raw(`SELECT first_key_id FROM observer_events`).Scan(&first)
	if first != old.KeyID {
		t.Fatal("rotation overwrote event provenance")
	}
	var tokens int64
	db.Raw(`SELECT tokens_milli FROM observer_device_budgets`).Scan(&tokens)
	if tokens != 18010 {
		t.Fatal("rotation reset budget", tokens)
	}
	if count(t, db, "observer_events") != 1 || count(t, db, "observer_requests") != 2 {
		t.Fatal("rotation duplicated evidence")
	}
}

func TestReceiveConcurrentRevocationIsSerialized(t *testing.T) {
	ctx := context.Background()
	db, svc, enrollment := receiveFixture(t)
	receiver := observerstore.NewReceiver(svc)
	body := receiveBody(t, enrollment, receiveEventID)
	results := make(chan observerstore.ReceiveResult, 12)
	revoked := make(chan error, 1)
	for i := 0; i < 12; i++ {
		r := receiveRequest(t, enrollment, body, reqID(i), pairingNow)
		go func() { results <- receiver.Receive(ctx, r, body, pairingNow) }()
	}
	go func() {
		revoked <- svc.RevokeBinding(ctx, enrollment.ReceiverID, enrollment.BindingVersion, pairingNow)
	}()
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	committed := int64(0)
	for i := 0; i < 12; i++ {
		got := <-results
		switch got.Status {
		case 200, 202:
			committed++
		case 403:
		default:
			t.Fatal("receive/revoke race", got)
		}
	}
	if got := receiver.Receive(ctx, receiveRequest(t, enrollment, body, reqID(20), pairingNow), body, pairingNow); got.Status != 403 {
		t.Fatal("post-revocation request accepted")
	}
	if count(t, db, "observer_requests") != committed {
		t.Fatal("ACK count differs from committed requests")
	}
	wantEvents := int64(0)
	if committed > 0 {
		wantEvents = 1
	}
	if count(t, db, "observer_events") != wantEvents {
		t.Fatal("partial authorization transaction")
	}
}
func TestReceiveHTTPPersistsBeforeReceipt(t *testing.T) {
	db, svc, enrollment := receiveFixture(t)
	// A real TLS connection exercises the new route, headers and body admission.
	server := httptest.NewUnstartedServer(nil)
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + server.Listener.Addr().String()
	// Credentials are scoped to the configured environment; origin belongs to the
	// listening authority and pairing grant, not the immutable event body.
	svc, err = observerstore.NewPairingService(db, "test", "synthetic", origin, cipher)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = observerhttp.Router(svc)
	server.StartTLS()
	defer server.Close()
	// This fixture's validity and envelope time must follow the actual HTTP clock.
	now := time.Now().UnixMilli()
	exec(t, db, `UPDATE observer_credentials SET expires_at_ms=? WHERE key_id=?`, now+60000, enrollment.KeyID)
	body := receiveBody(t, enrollment, receiveEventID)
	r := receiveRequest(t, enrollment, body, reqID(1), now)
	r.URL, _ = url.Parse(server.URL + observer.Path)
	r.Host = r.URL.Host
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatalf("HTTP status %d", response.StatusCode)
	}
	var ack observer.Receipt
	if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
		t.Fatal(err)
	}
	if ack.EventID != receiveEventID || ack.ReceiptStatus != "accepted" || response.Header.Get("Cache-Control") != "no-store" || count(t, db, "observer_events") != 1 || count(t, db, "observer_requests") != 1 {
		t.Fatal("receipt lacks committed evidence")
	}
}
