//go:build observersynthetic

package observerstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/example/epay-go/internal/config"
	"github.com/example/epay-go/internal/observerhttp"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/jwt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func confirmationActor(t *testing.T, f *matchFixture) int64 {
	t.Helper()
	var id int64
	if err := f.db.Raw(`SELECT id FROM admins LIMIT 1`).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}
func TestConfirmationPauseFreezesOldAndNewEvidence(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	ctx := context.Background()
	actor := confirmationActor(t, f)
	f.order(t, "before-pause", pairingNow)
	first, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
	result, err := f.authority.SetConfirmationPaused(ctx, f.enrollment.ReceiverID, true, "incident", actor, pairingNow+1100)
	if err != nil || result.FrozenEvents != 1 {
		t.Fatalf("pause=%+v error=%v", result, err)
	}
	f.process(t, first, pairingNow+1200, "evidence_insufficient")
	f.assertIncome(t, 0)
	second, _ := f.receive(t, pairingNow+1300, pairingNow+1300, func(m map[string]any) { m["amount_minor"] = 2000 })
	if _, err := f.authority.SetConfirmationPaused(ctx, f.enrollment.ReceiverID, false, "review_completed", actor, pairingNow+1400); err != nil {
		t.Fatal(err)
	}
	restarted, err := observerstore.NewMatcher(f.db, "test")
	if err != nil {
		t.Fatal(err)
	}
	f.matcher = restarted
	for _, id := range []int64{first, second} {
		d := f.process(t, id, pairingNow+1500, "evidence_insufficient")
		if d.Reason != "confirmation_paused" {
			t.Fatal("pause history reinterpreted")
		}
	}
	f.assertIncome(t, 0)
	f.order(t, "future", pairingNow+2000, "30")
	third, _ := f.receive(t, pairingNow+2100, pairingNow+2100, func(m map[string]any) { m["amount_minor"] = 3000 })
	f.process(t, third, pairingNow+2200, "matched")
	trace, err := f.authority.QueryEventTrace(ctx, f.enrollment.ReceiverID, 0, 2, actor)
	if err != nil || trace.Paused || len(trace.Events) != 2 || len(trace.Operations) != 2 || trace.Events[0].LedgerID == nil || trace.Events[0].OrderTradeNo == nil || *trace.Events[0].OrderTradeNo != "future" {
		t.Fatalf("trace=%+v err=%v", trace, err)
	}
	page, err := f.authority.QueryEventTrace(ctx, f.enrollment.ReceiverID, trace.NextBeforeEventPK, 2, actor)
	if err != nil || len(page.Events) != 1 || page.Events[0].EventPK != first {
		t.Fatal("trace pagination", err)
	}
	pausedCandidate := page.Events[0]
	if pausedCandidate.OrderTradeNo != nil || pausedCandidate.OrderStatus != nil || pausedCandidate.CandidateOrderTradeNo == nil || *pausedCandidate.CandidateOrderTradeNo != "before-pause" || pausedCandidate.CandidateOrderStatus == nil || *pausedCandidate.CandidateOrderStatus != 0 {
		t.Fatalf("paused candidate trace=%+v", pausedCandidate)
	}
	if trace.Events[0].OrderStatus == nil || *trace.Events[0].OrderStatus != 1 {
		t.Fatal("credited order status missing")
	}
	exec(t, f.db, `UPDATE orders SET status=2 WHERE trade_no='future'`)
	refunded, err := f.authority.QueryEventTrace(ctx, f.enrollment.ReceiverID, 0, 20, actor)
	if err != nil || refunded.Events[0].OrderStatus == nil || *refunded.Events[0].OrderStatus != 2 || refunded.Events[0].ProcessingStatus != "credited" || refunded.Events[0].LedgerID == nil {
		t.Fatalf("refund trace=%+v err=%v", refunded, err)
	}
	if count(t, f.db, "observer_consumptions") != 1 || count(t, f.db, "balance_records") != 1 {
		t.Fatal("refund trace changed historical consumption or income")
	}
	encoded, _ := json.Marshal(trace)
	for _, field := range []string{"device_id", "body_base64", "key_id", "signature", "key_ciphertext"} {
		if bytes.Contains(encoded, []byte(field)) {
			t.Fatal("sensitive trace field", field)
		}
	}
	if _, err := f.authority.QueryEventTrace(ctx, "prod-receiver", 0, 20, actor); !errors.Is(err, observerstore.ErrScope) {
		t.Fatal("trace crossed environment")
	}
	if _, err := f.authority.SetConfirmationPaused(ctx, f.enrollment.ReceiverID, true, "incident", actor, pairingNow+2300); err != nil {
		t.Fatal(err)
	}
	trace, err = f.authority.QueryEventTrace(ctx, f.enrollment.ReceiverID, 0, 20, actor)
	if err != nil || trace.Events[0].ProcessingStatus != "credited" {
		t.Fatal("pause rewrote credited history")
	}
	rejected(t, f.db, `UPDATE observer_confirmation_operations SET paused=false`)
	rejected(t, f.db, `DELETE FROM observer_confirmation_controls`)
	rejected(t, f.db, `UPDATE observer_confirmation_controls SET paused=false`)
}

func TestConfirmationPauseRollbackAndConcurrentMatching(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		f := newMatchFixture(t, "wxpay")
		actor := confirmationActor(t, f)
		f.order(t, "rollback", pairingNow)
		id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
		exec(t, f.db, `CREATE FUNCTION fail_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_pause_failure'; END $$; CREATE TRIGGER fail_pause BEFORE INSERT ON observer_match_decisions FOR EACH ROW EXECUTE FUNCTION fail_pause()`)
		if _, err := f.authority.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, true, "incident", actor, pairingNow+1100); err == nil {
			t.Fatal("pause failure did not roll back")
		}
		if count(t, f.db, "observer_confirmation_operations") != 0 || count(t, f.db, "observer_confirmation_controls") != 0 {
			t.Fatal("partial control commit")
		}
		exec(t, f.db, `DROP TRIGGER fail_pause ON observer_match_decisions`)
		f.process(t, id, pairingNow+1200, "matched")
		f.assertIncome(t, 1)
	})
	t.Run("concurrent", func(t *testing.T) {
		f := newMatchFixture(t, "wxpay")
		actor := confirmationActor(t, f)
		f.order(t, "concurrent", pairingNow)
		id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			_, err := f.authority.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, true, "incident", actor, pairingNow+1100)
			errs <- err
		})
		wg.Go(func() {
			<-start
			_, err := f.matcher.ProcessEvent(context.Background(), id, pairingNow+1100)
			errs <- err
		})
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		trace, err := f.authority.QueryEventTrace(context.Background(), f.enrollment.ReceiverID, 0, 20, actor)
		if err != nil {
			t.Fatal(err)
		}
		event := trace.Events[0]
		if event.ProcessingStatus == "credited" {
			f.assertIncome(t, 1)
		} else if event.ProcessingStatus == "needs_review" && event.Reason != nil && *event.Reason == "confirmation_paused" {
			f.assertIncome(t, 0)
		} else {
			t.Fatal("invalid race state")
		}
	})
}

func TestConfirmationAdminHTTPSPermissions(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	actor := confirmationActor(t, f)
	previous := config.Cfg
	config.Cfg = &config.Config{JWT: config.JWTConfig{Secret: strings.Repeat("synthetic", 8), ExpireHour: 1}}
	t.Cleanup(func() { config.Cfg = previous })
	server := httptest.NewUnstartedServer(nil)
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := observerstore.NewPairingService(f.db, "test", "synthetic", "https://"+server.Listener.Addr().String(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = observerhttp.Router(svc)
	server.StartTLS()
	defer server.Close()
	admin, _ := jwt.GenerateToken(actor, "admin", jwt.TokenTypeAdmin)
	merchant, _ := jwt.GenerateToken(actor, "merchant", jwt.TokenTypeMerchant)
	call := func(token, path, body string, wrongOrigin bool) int {
		t.Helper()
		r, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if wrongOrigin {
			r.Host = "wrong.example"
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	query := `{"schema_version":1,"receiver_id":"fixture-receiver-wx","before_event_pk":0,"limit":20}`
	pause := `{"schema_version":1,"receiver_id":"fixture-receiver-wx","paused":true,"reason_code":"maintenance"}`
	for _, token := range []string{"", merchant} {
		if got := call(token, "/api/admin/observer/events/query", query, false); got != 401 && got != 403 {
			t.Fatal("read authorization", got)
		}
		if got := call(token, "/api/admin/observer/confirmation", pause, false); got != 401 && got != 403 {
			t.Fatal("mutation authorization", got)
		}
	}
	if got := call(admin, "/api/admin/observer/events/query", query, true); got != 403 {
		t.Fatal("origin", got)
	}
	if got := call(admin, "/api/admin/observer/confirmation", strings.Replace(pause, `"paused":true`, `"paused":null`, 1), false); got != 400 {
		t.Fatal("null pause", got)
	}
	if got := call(admin, "/api/admin/observer/confirmation", strings.Replace(pause, `"paused":true`, `"paused":true,"actor_id":999`, 1), false); got != 400 {
		t.Fatal("actor injection", got)
	}
	if got := call(admin, "/api/admin/observer/confirmation", pause, false); got != 200 {
		t.Fatal("authorized pause", got)
	}
	if got := call(admin, "/api/admin/observer/events/query", query, false); got != 200 {
		t.Fatal("authorized trace", got)
	}
	var n int64
	f.db.Raw(`SELECT count(*) FROM observer_confirmation_operations WHERE actor_id=?`, actor).Scan(&n)
	if n != 1 {
		t.Fatal("actor audit")
	}
}
