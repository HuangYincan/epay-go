package observerstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"gorm.io/gorm"
)

// These are synthetic inputs over the real signed receiver and PostgreSQL.
// The test-only rule is not a real Alipay template or production approval.
const pilotTestRule = "synthetic-pilot-test-only"

func preparePilot(t *testing.T) (*notificationFixture, *model.Order) {
	t.Helper()
	f := newNotificationFixture(t, "alipay", "https://observer.example")
	exec(t, f.db, `INSERT INTO observer_notification_rules(parser_version,provider,evidence_ref,approved_by,enabled) VALUES (?,'alipay','synthetic-test-only',?,true)`, pilotTestRule, f.actor)
	expiry := time.UnixMilli(pairingNow + 600000)
	o := f.order(t, "pilot-order", pairingNow, &expiry, 0)
	exec(t, f.db, `UPDATE orders SET notify_url='https://merchant.example/api/user/epay/notify',checkout_type='qrcode',pay_url='https://qr.example/synthetic-pilot-only' WHERE id=?`, o.ID)
	return f, o
}
func armPilot(t *testing.T, f *notificationFixture, o *model.Order) {
	t.Helper()
	_, err := f.svc.ArmNotificationPilot(context.Background(), f.enrollment.ReceiverID, o.ID, pilotTestRule, f.actor, pairingNow+1000, pairingNow+301000)
	if err != nil {
		t.Fatal("arm", err)
	}
}
func pilotReceive(t *testing.T, f *notificationFixture, event, request int, observed, received int64, mutate func(map[string]any)) observerstore.ReceiveResult {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal(f.body(t, event, observed), &b); err != nil {
		t.Fatal(err)
	}
	b["parser_version"] = pilotTestRule
	delete(b, "provider_transaction_id")
	if mutate != nil {
		mutate(b)
	}
	body, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return observerstore.NewReceiver(f.svc).Receive(context.Background(), receiveRequest(t, f.enrollment, body, reqID(request), received), body, received)
}
func assertPilotCredit(t *testing.T, f *notificationFixture, o *model.Order) {
	t.Helper()
	if count(t, f.db, "observer_consumptions") != 1 || count(t, f.db, "balance_records") != 1 {
		t.Fatal("not exactly one settlement")
	}
	var saved model.Order
	if err := f.db.First(&saved, o.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != model.OrderStatusPaid || saved.ApiTradeNo != "" || saved.NotifyStatus != model.NotifyStatusPending {
		t.Fatal("order/callback state", saved.Status, saved.NotifyStatus)
	}
	var merchant model.Merchant
	if err := f.db.First(&merchant, f.merchant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !merchant.Balance.Equal(f.merchant.Balance.Add(o.Amount.Sub(o.Fee))) {
		t.Fatal("wrong income")
	}
	trace := f.trace(t)
	if trace.PolicyVersion == nil || *trace.PolicyVersion != observerstore.NotificationPilotPolicy {
		t.Fatal("missing pilot trace")
	}
}
func TestNotificationPilotCreditsExactlyOnceUnderConcurrentSignedReplay(t *testing.T) {
	f, o := preparePilot(t)
	armPilot(t, f, o)
	var wg sync.WaitGroup
	results := make(chan observerstore.ReceiveResult, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- pilotReceive(t, f, 1, 100+i, pairingNow+2000, pairingNow+2000, nil)
		}(i)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if (result.Status != 200 && result.Status != 202) || result.Receipt == nil || result.Receipt.ProcessingStatus != "credited" {
			t.Fatal("receipt", result)
		}
	}
	assertPilotCredit(t, f, o)
	result := pilotReceive(t, f, 2, 200, pairingNow+3000, pairingNow+3000, nil)
	if (result.Status != 200 && result.Status != 202) || result.Receipt.ProcessingStatus != "needs_review" {
		t.Fatal("second identity", result)
	}
	assertPilotCredit(t, f, o)
	if count(t, f.db, "observer_amount_claims") != 0 || count(t, f.db, "observer_channel_aliases") != 0 {
		t.Fatal("synthetic policy used")
	}
}
func TestNotificationPilotRejectsUnsafeEvidence(t *testing.T) {
	tests := []struct {
		name, reason       string
		observed, received int64
		setup              func(*testing.T, *notificationFixture, *model.Order)
		mutate             func(map[string]any)
	}{
		{name: "unknown-rule", reason: "unqualified_evidence", mutate: func(b map[string]any) { b["parser_version"] = "unknown" }},
		{name: "uncertain", reason: "unqualified_evidence", mutate: func(b map[string]any) { b["identity_confidence"] = "uncertain" }},
		{name: "invented-platform-id", reason: "unqualified_evidence", mutate: func(b map[string]any) { b["provider_transaction_id"] = "invented-test-reference" }},
		{name: "paused", reason: "confirmation_paused", setup: func(t *testing.T, f *notificationFixture, o *model.Order) {
			_, err := f.svc.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, true, "incident", f.actor, pairingNow+1500)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "disabled-pilot", reason: "scope_changed", setup: func(t *testing.T, f *notificationFixture, o *model.Order) {
			exec(t, f.db, `UPDATE observer_notification_pilots SET enabled=false`)
		}},
		{name: "disabled-rule", reason: "unqualified_evidence", setup: func(t *testing.T, f *notificationFixture, o *model.Order) {
			exec(t, f.db, `UPDATE observer_notification_rules SET enabled=false`)
		}},
		{name: "disabled-channel", reason: "scope_changed", setup: func(t *testing.T, f *notificationFixture, o *model.Order) {
			exec(t, f.db, `UPDATE channels SET status=0`)
		}},
		{name: "disabled-merchant", reason: "scope_changed", setup: func(t *testing.T, f *notificationFixture, o *model.Order) {
			exec(t, f.db, `UPDATE merchants SET status=0`)
		}},
		{name: "before-arm", reason: "observed_before_order", observed: pairingNow + 999},
		{name: "at-expiry", reason: "window_expired", observed: pairingNow + 301000, received: pairingNow + 301000},
		{name: "stale", reason: "stale_observation", observed: pairingNow + 2000, received: pairingNow + 122001},
		{name: "future-clock", reason: "clock_skew", observed: pairingNow + 4001, received: pairingNow + 2000},
		{name: "deleted-order", reason: "scope_changed", setup: func(t *testing.T, f *notificationFixture, o *model.Order) {
			if err := f.db.Delete(o).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, o := preparePilot(t)
			armPilot(t, f, o)
			if tt.setup != nil {
				tt.setup(t, f, o)
			}
			observed, received := tt.observed, tt.received
			if observed == 0 {
				observed = pairingNow + 2000
			}
			if received == 0 {
				received = pairingNow + 2000
			}
			result := pilotReceive(t, f, 1, 1, observed, received, tt.mutate)
			if (result.Status != 200 && result.Status != 202) || result.Receipt.ProcessingStatus != "needs_review" {
				t.Fatal(result)
			}
			trace := f.trace(t)
			if trace.Reason == nil || *trace.Reason != tt.reason {
				t.Fatalf("decision %#v want %s", trace, tt.reason)
			}
			f.noSettlement(t)
			// Corrected payload is an event conflict, never an evidence upgrade.
			pilotReceive(t, f, 1, 2, observed, received, nil)
			f.noSettlement(t)
		})
	}
}
func TestNotificationPilotRegistrationRejectsHistoryAndMissingApproval(t *testing.T) {
	tests := []string{"no-rule", "disabled-rule", "same-amount-unpaid", "same-amount-paid", "same-amount-deleted", "earlier-event", "expiry-after-checkout", "too-long", "wrong-admin", "wrong-provider", "alias"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			f, o := preparePilot(t)
			parser, actor, expires := pilotTestRule, f.actor, pairingNow+301000
			switch name {
			case "no-rule":
				parser = "not-approved"
			case "disabled-rule":
				exec(t, f.db, `UPDATE observer_notification_rules SET enabled=false`)
			case "same-amount-unpaid", "same-amount-paid", "same-amount-deleted":
				expiry := time.UnixMilli(pairingNow + 600000)
				status := int8(0)
				if name == "same-amount-paid" {
					status = 1
				}
				history := f.order(t, "history", pairingNow-1000, &expiry, status)
				if name == "same-amount-deleted" {
					f.db.Delete(history)
				}
			case "earlier-event":
				pilotReceive(t, f, 1, 1, pairingNow, pairingNow, nil)
			case "expiry-after-checkout":
				exec(t, f.db, `UPDATE orders SET checkout_expires_at=? WHERE id=?`, time.UnixMilli(pairingNow+200000), o.ID)
			case "too-long":
				expires = pairingNow + 601001
			case "wrong-admin":
				actor = 999999
			case "wrong-provider":
				exec(t, f.db, `UPDATE orders SET pay_type='wxpay' WHERE id=?`, o.ID)
			case "alias":
				f.link(t)
			}
			if _, err := f.svc.ArmNotificationPilot(context.Background(), f.enrollment.ReceiverID, o.ID, parser, actor, pairingNow+1000, expires); err == nil {
				t.Fatal("unsafe registration accepted")
			}
			if count(t, f.db, "observer_notification_pilots") != 0 {
				t.Fatal("failed registration persisted")
			}
			f.noSettlement(t)
		})
	}
}
func TestNotificationPilotRetainsAmountAndOrderScope(t *testing.T) {
	f, o := preparePilot(t)
	armPilot(t, f, o)
	for _, query := range []string{`DELETE FROM observer_notification_pilots`, `UPDATE observer_notification_pilots SET expires_at_ms=expires_at_ms+1`, `UPDATE observer_notification_rules SET evidence_ref='changed'`, `UPDATE orders SET fee=1 WHERE id=%d`, `UPDATE orders SET amount=11 WHERE id=%d`, `UPDATE orders SET status=1 WHERE id=%d`} {
		if len(query) > 0 && query[len(query)-1] == 'd' {
			query = fmt.Sprintf(query, o.ID)
		}
		rejected(t, f.db, query)
	}
	expiry := time.UnixMilli(pairingNow + 600000)
	other := &model.Order{TradeNo: "collision", OutTradeNo: "collision", MerchantID: f.merchant.ID, ChannelID: f.channel.ID, PayType: "alipay", Amount: o.Amount, RealAmount: o.RealAmount, CheckoutExpiresAt: &expiry}
	if err := f.db.Create(other).Error; err == nil {
		t.Fatal("reserved amount reused")
	}
	if err := f.db.Transaction(func(tx *gorm.DB) error { return observerstore.RejectLegacySettlement(tx, o) }); err != observerstore.ErrObserverSettlement {
		t.Fatal("legacy settlement permitted", err)
	}
}
func TestNotificationPilotCommitFailureRollsBackAndCanRetry(t *testing.T) {
	f, o := preparePilot(t)
	armPilot(t, f, o)
	exec(t, f.db, `CREATE FUNCTION fail_pilot_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_commit_fault'; END $$; CREATE CONSTRAINT TRIGGER fail_pilot_commit AFTER INSERT ON observer_consumptions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_pilot_commit()`)
	result := pilotReceive(t, f, 1, 1, pairingNow+2000, pairingNow+2000, nil)
	if result.Status != 503 {
		t.Fatal("ack before commit", result)
	}
	f.noSettlement(t)
	if count(t, f.db, "observer_events") != 0 || count(t, f.db, "observer_requests") != 0 || count(t, f.db, "observer_candidate_snapshots") != 0 {
		t.Fatal("partial receipt")
	}
	exec(t, f.db, `DROP TRIGGER fail_pilot_commit ON observer_consumptions`)
	result = pilotReceive(t, f, 1, 1, pairingNow+2000, pairingNow+2000, nil)
	if (result.Status != 200 && result.Status != 202) || result.Receipt.ProcessingStatus != "credited" {
		t.Fatal("retry", result)
	}
	assertPilotCredit(t, f, o)
}

func TestNotificationPilotDoesNotUpgradeOldReviewAfterRegistrationOrResume(t *testing.T) {
	f, o := preparePilot(t)
	first := pilotReceive(t, f, 1, 1, pairingNow, pairingNow, nil)
	if first.Receipt == nil || first.Receipt.ProcessingStatus != "needs_review" {
		t.Fatal(first)
	}
	if _, err := f.svc.ArmNotificationPilot(context.Background(), f.enrollment.ReceiverID, o.ID, pilotTestRule, f.actor, pairingNow+1000, pairingNow+301000); err == nil {
		t.Fatal("old evidence authorized")
	}
	replay := pilotReceive(t, f, 1, 2, pairingNow, pairingNow+2000, nil)
	if replay.Receipt == nil || replay.Receipt.ProcessingStatus != "needs_review" {
		t.Fatal(replay)
	}
	f.noSettlement(t)

	g, order := preparePilot(t)
	armPilot(t, g, order)
	if _, err := g.svc.SetConfirmationPaused(context.Background(), g.enrollment.ReceiverID, true, "incident", g.actor, pairingNow+1100); err != nil {
		t.Fatal(err)
	}
	pilotReceive(t, g, 1, 1, pairingNow+2000, pairingNow+2000, nil)
	if _, err := g.svc.SetConfirmationPaused(context.Background(), g.enrollment.ReceiverID, false, "review_completed", g.actor, pairingNow+2100); err != nil {
		t.Fatal(err)
	}
	replay = pilotReceive(t, g, 1, 2, pairingNow+2000, pairingNow+2200, nil)
	if replay.Receipt == nil || replay.Receipt.ProcessingStatus != "needs_review" {
		t.Fatal("resume upgraded", replay)
	}
	pilotReceive(t, g, 2, 3, pairingNow+2300, pairingNow+2300, nil)
	g.noSettlement(t)
}

func TestNotificationPilotCannotCrossCredentialScope(t *testing.T) {
	for _, name := range []string{"receiver", "source", "binding", "revoked-binding"} {
		t.Run(name, func(t *testing.T) {
			f, o := preparePilot(t)
			armPilot(t, f, o)
			mutate := func(b map[string]any) {
				switch name {
				case "receiver":
					b["receiver_id"] = "another-receiver"
				case "source":
					b["source_class"] = "synthetic"
				case "binding":
					b["binding_version"] = 999
				}
			}
			if name == "revoked-binding" {
				exec(t, f.db, `UPDATE observer_bindings SET active=false WHERE environment='prod'`)
			}
			result := pilotReceive(t, f, 1, 1, pairingNow+2000, pairingNow+2000, mutate)
			if result.Status >= 200 && result.Status < 300 {
				t.Fatal("scope accepted", result)
			}
			f.noSettlement(t)
		})
	}
}

func TestNotificationPilotStaticCodeMayHaveNoCheckoutExpiry(t *testing.T) {
	f, o := preparePilot(t)
	exec(t, f.db, `UPDATE orders SET checkout_expires_at=NULL WHERE id=?`, o.ID)
	armPilot(t, f, o)
	result := pilotReceive(t, f, 1, 1, pairingNow+2000, pairingNow+2000, nil)
	if result.Receipt == nil || result.Receipt.ProcessingStatus != "credited" {
		t.Fatal(result)
	}
	assertPilotCredit(t, f, o)
}

func TestNotificationPilotRegistrationSerializesWithOrderCreation(t *testing.T) {
	for i := 0; i < 6; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f, o := preparePilot(t)
			start := make(chan struct{})
			armed := make(chan error, 1)
			created := make(chan error, 1)
			other := *o
			other.ID = 0
			other.TradeNo = "concurrent-order"
			other.OutTradeNo = "concurrent-order"
			go func() {
				<-start
				_, err := f.svc.ArmNotificationPilot(context.Background(), f.enrollment.ReceiverID, o.ID, pilotTestRule, f.actor, pairingNow+1000, pairingNow+301000)
				armed <- err
			}()
			go func() { <-start; created <- f.db.Create(&other).Error }()
			close(start)
			a, c := <-armed, <-created
			if (a == nil) == (c == nil) {
				t.Fatal("registration and conflicting order must have exactly one winner", a, c)
			}
			if a == nil && count(t, f.db, "orders") != 1 {
				t.Fatal("registered over conflicting history")
			}
			if c == nil && count(t, f.db, "observer_notification_pilots") != 0 {
				t.Fatal("failed registration persisted")
			}
			f.noSettlement(t)
		})
	}
}
