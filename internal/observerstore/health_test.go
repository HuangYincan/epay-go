package observerstore_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/example/epay-go/internal/observerhttp"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

var healthBody = []byte(`{"schema_version":1}`)

func healthRequest(t *testing.T, enrollment observer.Enrollment, sent int64) *http.Request {
	t.Helper()
	r := receiveRequest(t, enrollment, healthBody, reqID(1), sent)
	r.URL.Path = observer.HealthPath
	key, err := base64.RawURLEncoding.DecodeString(enrollment.KeyBase64URL)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	digest := sha256.Sum256(healthBody)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.Join([]string{"epay-observer-health-v1", "POST", observer.HealthPath, enrollment.KeyID, strconv.FormatInt(sent, 10), reqID(1), hex.EncodeToString(digest[:])}, "\n")))
	r.Header.Set("X-Observer-Signature", hex.EncodeToString(mac.Sum(nil)))
	return r
}

func TestHealthReadOnlyBudgetAndCurrentAuthorization(t *testing.T) {
	db, svc, enrollment := receiveFixture(t)
	receiver := observerstore.NewReceiver(svc)
	ctx := context.Background()
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	other, err := observerstore.NewPairingService(db, "prod", "notification", "https://prod.example", cipher)
	if err != nil {
		t.Fatal(err)
	}
	if got := observerstore.NewReceiver(other).CheckHealth(ctx, healthRequest(t, enrollment, pairingNow), healthBody, pairingNow); got.Status != 403 || got.Health != nil {
		t.Fatal("cross-environment health accepted", got)
	}
	for i := 0; i < 20; i++ {
		got := receiver.CheckHealth(ctx, healthRequest(t, enrollment, pairingNow), healthBody, pairingNow)
		if got.Status != 200 || got.Health == nil || got.Health.Status != "authorized" || got.Receipt != nil {
			t.Fatal(got)
		}
	}
	got := observerstore.NewReceiver(pairingService(t, db)).CheckHealth(ctx, healthRequest(t, enrollment, pairingNow), healthBody, pairingNow)
	if got.Status != 429 || got.RetryAfterSeconds != 1 {
		t.Fatal("health replay bypassed persistent budget", got)
	}
	for _, table := range []string{"observer_events", "observer_requests", "orders", "balance_records"} {
		if count(t, db, table) != 0 {
			t.Fatal("health created business data", table)
		}
	}
	if got := receiver.CheckHealth(ctx, healthRequest(t, enrollment, enrollment.ExpiresAtMs), healthBody, enrollment.ExpiresAtMs); got.Status != 401 {
		t.Fatal("expired health authorized", got)
	}
	if err := svc.RevokeBinding(ctx, enrollment.ReceiverID, enrollment.BindingVersion, pairingNow+1); err != nil {
		t.Fatal(err)
	}
	if got := receiver.CheckHealth(ctx, healthRequest(t, enrollment, pairingNow), healthBody, pairingNow+1); got.Status != 403 || got.Health != nil {
		t.Fatal("health replay bypassed revocation", got)
	}
}

func TestHealthCommitFailureCannotReportAuthorized(t *testing.T) {
	db, svc, enrollment := receiveFixture(t)
	exec(t, db, `CREATE FUNCTION reject_health_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test health rollback'; END $$`)
	exec(t, db, `CREATE CONSTRAINT TRIGGER health_commit_failure AFTER INSERT ON observer_device_budgets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_health_commit()`)
	got := observerstore.NewReceiver(svc).CheckHealth(context.Background(), healthRequest(t, enrollment, pairingNow), healthBody, pairingNow)
	if got.Status != 503 || got.Health != nil || count(t, db, "observer_device_budgets") != 0 {
		t.Fatal("health response escaped failed commit", got)
	}
}

func TestHealthActualTLSDoesNotCreateEvidence(t *testing.T) {
	db, _, enrollment := receiveFixture(t)
	server := httptest.NewUnstartedServer(nil)
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := observerstore.NewPairingService(db, "test", "synthetic", "https://"+server.Listener.Addr().String(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = observerhttp.Router(svc)
	server.StartTLS()
	defer server.Close()
	now := time.Now().UnixMilli()
	exec(t, db, `UPDATE observer_credentials SET expires_at_ms=? WHERE key_id=?`, now+60000, enrollment.KeyID)
	r := healthRequest(t, enrollment, now)
	r.URL, _ = url.Parse(server.URL + observer.HealthPath)
	r.Host = r.URL.Host
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result observer.HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" || result.Status != "authorized" || result.ServerTimeMs < now {
		t.Fatal("bad TLS health response")
	}
	for _, table := range []string{"observer_events", "observer_requests", "orders", "balance_records"} {
		if count(t, db, table) != 0 {
			t.Fatal("TLS health changed business data", table)
		}
	}
}
