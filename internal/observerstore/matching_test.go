//go:build observersynthetic

package observerstore_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example/epay-go/internal/observerhttp"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type matchFixture struct {
	db         *gorm.DB
	authority  *observerstore.PairingService
	receiver   *observerstore.Receiver
	enrollment observer.Enrollment
	matcher    *observerstore.Matcher
	merchant   model.Merchant
	channel    model.Channel
	sequence   int
}

func newMatchFixture(t *testing.T, provider string, clocks ...int64) *matchFixture {
	t.Helper()
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "test")
	db, svc, actor := pairingDB(t)
	now := pairingNow
	if len(clocks) != 0 {
		now = clocks[0]
	}
	receiverID := "fixture-receiver-wx"
	if provider == "alipay" {
		receiverID = "fixture-receiver-ali"
	}
	_, token, err := svc.CreateGrant(context.Background(), receiverID, "replace", actor, now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := svc.Redeem(context.Background(), token, request(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(context.Background(), token, request(), now); err != nil {
		t.Fatal(err)
	}
	matcher, err := observerstore.NewMatcher(db, "test")
	if err != nil {
		t.Fatal(err)
	}
	f := &matchFixture{db: db, authority: svc, receiver: observerstore.NewReceiver(svc), enrollment: enrollment, matcher: matcher,
		merchant: model.Merchant{Username: "synthetic-match-merchant", Password: "unused", ApiKey: "synthetic-match-key", Status: 1, Balance: decimal.RequireFromString("3.25")},
		channel:  model.Channel{Name: "synthetic-match-channel", Plugin: "epay", Status: 1, AppType: "qrcode"}}
	if err := db.Create(&f.merchant).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&f.channel).Error; err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO observer_synthetic_merchants(merchant_id,environment,enabled) VALUES (?,'test',true)`, f.merchant.ID)
	exec(t, db, `INSERT INTO observer_channel_aliases VALUES (?,'test',?,?)`, f.channel.ID, receiverID, provider)
	return f
}

// Prepare an ordinary immutable order/claim in the real transaction, with an
// explicit clock for boundary tests. HTTP creation is exercised separately by SPEC-021.
func (f *matchFixture) order(t *testing.T, no string, created int64, amounts ...string) *model.Order {
	t.Helper()
	expires := time.UnixMilli(created + 600000)
	amount := decimal.NewFromInt(10)
	if len(amounts) > 0 {
		amount = decimal.RequireFromString(amounts[0])
	}
	o := &model.Order{BaseModel: model.BaseModel{CreatedAt: time.UnixMilli(created)}, TradeNo: no, OutTradeNo: no, MerchantID: f.merchant.ID, ChannelID: f.channel.ID, PayType: f.enrollment.Provider, Amount: amount, RealAmount: amount, Fee: decimal.RequireFromString("0.10"), ObserverPayExpiresAt: &expires, Name: "synthetic ordinary order"}
	err := f.db.Transaction(func(tx *gorm.DB) error {
		scope, err := observerstore.LockOrderScope(tx, o.ChannelID, o.MerchantID, o.PayType)
		if err != nil {
			return err
		}
		if err := tx.Create(o).Error; err != nil {
			return err
		}
		return scope.Claim(tx, o)
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func (f *matchFixture) receive(t *testing.T, observed, received int64, mutate func(map[string]any)) (int64, []byte) {
	t.Helper()
	f.sequence++
	parser := "fixture-wx-v1"
	if f.enrollment.Provider == "alipay" {
		parser = "fixture-ali-v1"
	}
	data := map[string]any{"schema_version": 1, "event_id": fmt.Sprintf("%08x-1111-4111-8111-111111111111", f.sequence), "device_id": f.enrollment.DeviceID, "receiver_id": f.enrollment.ReceiverID, "binding_version": f.enrollment.BindingVersion, "provider": f.enrollment.Provider, "amount_minor": 1000, "currency": "CNY", "observed_at_ms": observed, "parser_version": parser, "source_class": "synthetic", "identity_confidence": "distinct"}
	if mutate != nil {
		mutate(data)
	}
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	result := f.receiver.Receive(context.Background(), receiveRequest(t, f.enrollment, body, reqID(f.sequence), received), body, received)
	if result.Status != 202 {
		t.Fatalf("authenticated receipt failed: %+v", result)
	}
	var id int64
	f.db.Raw(`SELECT id FROM observer_events WHERE event_id=?`, data["event_id"]).Scan(&id)
	if id == 0 {
		t.Fatal("event not stored")
	}
	return id, body
}
func (f *matchFixture) process(t *testing.T, id, now int64, outcome string) observerstore.MatchingDecision {
	t.Helper()
	d, err := f.matcher.ProcessEvent(context.Background(), id, now)
	if err != nil || d.Outcome != outcome {
		t.Fatalf("decision=%+v err=%v want=%s", d, err, outcome)
	}
	return d
}
func (f *matchFixture) assertIncome(t *testing.T, n int64) {
	t.Helper()
	if count(t, f.db, "observer_consumptions") != n || count(t, f.db, "balance_records") != n {
		t.Fatal("income/consumption count")
	}
	var m model.Merchant
	if err := f.db.First(&m, f.merchant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !m.Balance.Equal(decimal.RequireFromString("3.25").Add(decimal.RequireFromString("9.90").Mul(decimal.NewFromInt(n)))) {
		t.Fatal("incorrect exact balance")
	}
}
func TestMatchBothProvidersAtomicIncomeAndCreditedDuplicateACK(t *testing.T) {
	for _, provider := range []string{"wxpay", "alipay"} {
		t.Run(provider, func(t *testing.T) {
			f := newMatchFixture(t, provider)
			o := f.order(t, "normal", pairingNow)
			id, body := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
			d := f.process(t, id, pairingNow+1000, "matched")
			if d.OrderID == nil || *d.OrderID != o.ID {
				t.Fatal("wrong audit order")
			}
			f.assertIncome(t, 1)
			var stored struct {
				Status, NotifyStatus int
				MissingReference     bool
			}
			f.db.Raw(`SELECT status,notify_status,api_trade_no IS NULL AS missing_reference FROM orders WHERE id=?`, o.ID).Scan(&stored)
			if stored.Status != model.OrderStatusPaid || stored.NotifyStatus != model.NotifyStatusPending || !stored.MissingReference {
				t.Fatal("incorrect settlement metadata")
			}
			f.process(t, id, pairingNow+2000, "matched")
			f.assertIncome(t, 1)
			replay := f.receiver.Receive(context.Background(), receiveRequest(t, f.enrollment, body, reqID(99), pairingNow+2000), body, pairingNow+2000)
			if replay.Status != 200 || replay.Receipt.ProcessingStatus != "credited" || replay.Receipt.ReceiptStatus != "duplicate" {
				t.Fatalf("credited replay ACK: %+v", replay)
			}
			second, _ := f.receive(t, pairingNow+3000, pairingNow+3000, nil)
			f.process(t, second, pairingNow+3000, "duplicate_suspected")
			f.assertIncome(t, 1)
		})
	}
}
func TestMatchConcurrentSameEventAndDistinctEventsCreditOnce(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(fmt.Sprint(distinct), func(t *testing.T) {
			f := newMatchFixture(t, "wxpay")
			f.order(t, "concurrent", pairingNow)
			first, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
			ids := []int64{first}
			for i := 1; i < 12; i++ {
				if distinct {
					id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
					ids = append(ids, id)
				} else {
					ids = append(ids, first)
				}
			}
			var wg sync.WaitGroup
			errs := make(chan error, 12)
			for _, id := range ids {
				wg.Add(1)
				go func(id int64) {
					defer wg.Done()
					_, err := f.matcher.ProcessEvent(context.Background(), id, pairingNow+1000)
					errs <- err
				}(id)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			f.assertIncome(t, 1)
		})
	}
}
func TestMatchWindowAndClockBoundaries(t *testing.T) {
	cases := []struct {
		name                        string
		observed, received, process int64
		want                        string
	}{
		{"normal", 1000, 1000, 1000, "matched"}, {"last-ms", 599999, 599999, 600001, "matched"}, {"expiry", 600000, 600000, 600000, "expired"},
		{"future-limit", 3000, 1000, 1000, "matched"}, {"future-over", 3001, 1000, 1000, "clock_error"},
		{"stale-limit", 1000, 121000, 121000, "matched"}, {"stale-over", 1000, 121001, 121001, "stale_observation"},
		{"before-order", -1, 1000, 1000, "clock_error"}, {"server-backward", 1000, 1000, 999, "clock_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMatchFixture(t, "wxpay")
			f.order(t, "boundary", pairingNow)
			id, _ := f.receive(t, pairingNow+c.observed, pairingNow+c.received, nil)
			f.process(t, id, pairingNow+c.process, c.want)
			n := int64(0)
			if c.want == "matched" {
				n = 1
			}
			f.assertIncome(t, n)
		})
	}
}
func TestMatchCannotBindFutureOrderInSameMillisecondOrClearHistory(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	now := pairingNow + 1000
	id, _ := f.receive(t, now, now, nil)
	f.order(t, "created-after-receipt-same-ms", now)
	f.process(t, id, now, "unmatched")
	f.assertIncome(t, 0)
	later, _ := f.receive(t, now+1, now+1, nil)
	d := f.process(t, later, now+1, "duplicate_suspected")
	if d.Reason != "earlier_evidence" {
		t.Fatal("historical unmatched evidence forgotten")
	}
	f.assertIncome(t, 0)
	f.process(t, id, now+2, "unmatched")
}
func TestMatchUnqualifiedEvidenceAndRevocationRemainUncredited(t *testing.T) {
	for _, kind := range []string{"uncertain", "parser", "provider-reference", "conflict", "binding", "credential", "environment", "receiver", "merchant"} {
		t.Run(kind, func(t *testing.T) {
			f := newMatchFixture(t, "wxpay")
			f.order(t, "reject", pairingNow)
			id, body := f.receive(t, pairingNow+1000, pairingNow+1000, func(e map[string]any) {
				switch kind {
				case "uncertain":
					e["identity_confidence"] = "uncertain"
				case "parser":
					e["parser_version"] = "unknown-v1"
				case "provider-reference":
					e["provider_transaction_id"] = "invented-negative-fixture"
				}
			})
			switch kind {
			case "conflict":
				var e map[string]any
				json.Unmarshal(body, &e)
				e["amount_minor"] = 1001
				changed, _ := json.Marshal(e)
				r := f.receiver.Receive(context.Background(), receiveRequest(t, f.enrollment, changed, reqID(90), pairingNow+1001), changed, pairingNow+1001)
				if r.Status != 409 {
					t.Fatal("conflict not detected")
				}
			case "binding":
				exec(t, f.db, `UPDATE observer_bindings SET active=false WHERE environment='test' AND receiver_id=?`, f.enrollment.ReceiverID)
			case "credential":
				exec(t, f.db, `UPDATE observer_credentials SET state='revoked',revoked_at_ms=? WHERE key_id=?`, pairingNow+1001, f.enrollment.KeyID)
			case "environment":
				exec(t, f.db, `UPDATE observer_environments SET enabled=false WHERE environment='test'`)
			case "receiver":
				exec(t, f.db, `UPDATE observer_receivers SET enabled=false WHERE environment='test'`)
			case "merchant":
				exec(t, f.db, `UPDATE merchants SET status=0 WHERE id=?`, f.merchant.ID)
			}
			f.process(t, id, pairingNow+1002, "evidence_insufficient")
			f.assertIncome(t, 0)
		})
	}
}
func TestMatchTransactionFailuresLeavePendingAndRetryExactlyOnce(t *testing.T) {
	for _, table := range []string{"orders", "merchants", "balance_records", "observer_match_decisions", "observer_consumptions", "observer_events", "deferred"} {
		t.Run(table, func(t *testing.T) {
			f := newMatchFixture(t, "wxpay")
			f.order(t, "rollback", pairingNow)
			id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
			exec(t, f.db, `CREATE FUNCTION fail_matching() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic matching failure'; END $$`)
			if table == "deferred" {
				exec(t, f.db, `CREATE CONSTRAINT TRIGGER fail_matching AFTER INSERT ON observer_consumptions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_matching()`)
			} else {
				exec(t, f.db, `CREATE TRIGGER fail_matching BEFORE INSERT OR UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_matching()`)
			}
			if _, err := f.matcher.ProcessEvent(context.Background(), id, pairingNow+1000); err == nil {
				t.Fatal("failed transaction returned success")
			}
			f.assertIncome(t, 0)
			if count(t, f.db, "observer_match_decisions") != 0 {
				t.Fatal("partial decision escaped rollback")
			}
			var state string
			f.db.Raw(`SELECT processing_status FROM observer_events WHERE id=?`, id).Scan(&state)
			if state != "pending" {
				t.Fatal("failed event not recoverable")
			}
			target := table
			if table == "deferred" {
				target = "observer_consumptions"
			}
			exec(t, f.db, `DROP TRIGGER fail_matching ON `+target)
			f.process(t, id, pairingNow+1000, "matched")
			f.assertIncome(t, 1)
		})
	}
}
func TestMatchCannotCrossConfiguredEnvironment(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	f.order(t, "environment", pairingNow)
	id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "other")
	if _, err := f.matcher.ProcessEvent(context.Background(), id, pairingNow+1000); !errors.Is(err, observerstore.ErrSyntheticDisabled) {
		t.Fatal("runtime scope changed silently")
	}
	f.assertIncome(t, 0)
}

func TestMatchLegacyCandidateAmbiguityAndMissingClaim(t *testing.T) {
	for _, n := range []int{1, 2} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newMatchFixture(t, "wxpay")
			ch := model.Channel{Name: "legacy-history", Plugin: "epay", Status: 1}
			if err := f.db.Create(&ch).Error; err != nil {
				t.Fatal(err)
			}
			for i := 0; i < n; i++ {
				o := model.Order{BaseModel: model.BaseModel{CreatedAt: time.UnixMilli(pairingNow)}, TradeNo: fmt.Sprint("legacy-", i), OutTradeNo: fmt.Sprint("legacy-", i), MerchantID: f.merchant.ID, ChannelID: ch.ID, PayType: "wxpay", Amount: decimal.NewFromInt(10), RealAmount: decimal.NewFromInt(10)}
				if err := f.db.Create(&o).Error; err != nil {
					t.Fatal(err)
				}
			}
			exec(t, f.db, `INSERT INTO observer_channel_aliases VALUES (?,'test',?,'wxpay')`, ch.ID, f.enrollment.ReceiverID)
			id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
			want := "evidence_insufficient"
			if n == 2 {
				want = "ambiguous"
			}
			d := f.process(t, id, pairingNow+1000, want)
			if d.CandidateCount != int64(n) {
				t.Fatal("wrong frozen candidate count")
			}
			f.assertIncome(t, 0)
		})
	}
}
func TestMatchDoesNotRevivePaidOrRefundedOrders(t *testing.T) {
	for _, state := range []int{1, 2} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			f := newMatchFixture(t, "wxpay")
			o := f.order(t, "already-settled", pairingNow)
			// A prepared historical status, not a claim that an actual refund was executed.
			exec(t, f.db, `UPDATE orders SET status=? WHERE id=?`, state, o.ID)
			id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
			f.process(t, id, pairingNow+1000, "duplicate_suspected")
			f.assertIncome(t, 0)
		})
	}
}
func TestMatchStoragePrimitiveAloneCannotAuthorizeSettlement(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	f.order(t, "primitive", pairingNow)
	var data map[string]any
	json.Unmarshal(receiveBody(t, f.enrollment, receiveEventID), &data)
	data["identity_confidence"] = "distinct"
	data["observed_at_ms"] = pairingNow + 1000
	body, _ := json.Marshal(data)
	r := receiveRequest(t, f.enrollment, body, reqID(99), pairingNow+1000)
	key, _ := base64.RawURLEncoding.DecodeString(f.enrollment.KeyBase64URL)
	defer clear(key)
	verified, err := observer.VerifyRequest(r, body, f.enrollment.KeyID, key, pairingNow+1000)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observerstore.RecordEvidence(context.Background(), f.db, "test", f.enrollment.KeyID, verified, pairingNow+1000)
	if err != nil {
		t.Fatal(err)
	}
	d := f.process(t, evidence.EventPK, pairingNow+1000, "evidence_insufficient")
	if d.Reason != "missing_receipt" {
		t.Fatal("storage primitive treated as authenticated receive")
	}
	f.assertIncome(t, 0)
}
func TestMatchPendingWorkerRecoversFromNewInstance(t *testing.T) {
	now := time.Now().UnixMilli()
	f := newMatchFixture(t, "wxpay", now)
	f.order(t, "pending-recovery", now)
	f.receive(t, now, now, nil)
	restarted, err := observerstore.NewMatcher(f.db, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ProcessPending(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ProcessPending(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	f.assertIncome(t, 1)
}
func TestMatchNotificationSourceNeverQualifiesEvenInTaggedBuild(t *testing.T) {
	db, _, actor := pairingDB(t)
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "prod")
	exec(t, db, `UPDATE observer_environments SET enabled=true WHERE environment='prod'`)
	exec(t, db, `UPDATE observer_receivers SET enabled=true WHERE environment='prod'`)
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := observerstore.NewPairingService(db, "prod", "notification", "https://observer.example", cipher)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := svc.CreateGrant(context.Background(), "prod-receiver", "replace", actor, pairingNow)
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
	var data map[string]any
	json.Unmarshal(receiveBody(t, e, receiveEventID), &data)
	data["identity_confidence"] = "distinct"
	body, _ := json.Marshal(data)
	result := observerstore.NewReceiver(svc).Receive(context.Background(), receiveRequest(t, e, body, reqID(1), pairingNow), body, pairingNow)
	if result.Status != 202 {
		t.Fatal("negative notification-source protocol receipt failed")
	}
	var id int64
	db.Raw(`SELECT id FROM observer_events WHERE event_id=?`, receiveEventID).Scan(&id)
	matcher, err := observerstore.NewMatcher(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	d, err := matcher.ProcessEvent(context.Background(), id, pairingNow)
	if err != nil || d.Outcome != "evidence_insufficient" {
		t.Fatalf("notification source qualified: %+v %v", d, err)
	}
	if count(t, db, "observer_consumptions") != 0 || count(t, db, "balance_records") != 0 {
		t.Fatal("notification source credited")
	}
}
func TestMatchAuditConstraintsRejectIncompleteOrMutableSettlement(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	f.order(t, "audit", pairingNow)
	id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
	rejected(t, f.db, `UPDATE observer_events SET processing_status='credited' WHERE id=?`, id)
	f.process(t, id, pairingNow+1000, "matched")
	for _, q := range []string{`DELETE FROM observer_consumptions`, `UPDATE observer_match_decisions SET outcome='unmatched'`, `UPDATE observer_events SET processing_status='pending'`, `UPDATE balance_records SET amount=1`, `UPDATE orders SET fee=1`} {
		rejected(t, f.db, q)
	}
	f.assertIncome(t, 1)
}

func TestMatchActualTLSReceiptThenCreditedReplay(t *testing.T) {
	now := time.Now().UnixMilli()
	f := newMatchFixture(t, "wxpay", now)
	f.order(t, "tls-receipt", now)
	server := httptest.NewUnstartedServer(nil)
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := observerstore.NewPairingService(f.db, "test", "synthetic", "https://"+server.Listener.Addr().String(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = observerhttp.Router(authority)
	server.StartTLS()
	defer server.Close()
	var data map[string]any
	json.Unmarshal(receiveBody(t, f.enrollment, receiveEventID), &data)
	data["identity_confidence"] = "distinct"
	data["observed_at_ms"] = time.Now().UnixMilli()
	body, _ := json.Marshal(data)
	send := func(n int) (int, observer.Receipt) {
		r := receiveRequest(t, f.enrollment, body, reqID(n), time.Now().UnixMilli())
		r.URL, _ = url.Parse(server.URL + observer.Path)
		r.Host = r.URL.Host
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var receipt observer.Receipt
		if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, receipt
	}
	status, first := send(1)
	if status != 202 || first.ReceiptStatus != "accepted" || first.ProcessingStatus != "pending" {
		t.Fatalf("first TLS receipt: %d %+v", status, first)
	}
	var id int64
	f.db.Raw(`SELECT id FROM observer_events WHERE event_id=?`, receiveEventID).Scan(&id)
	f.process(t, id, time.Now().UnixMilli(), "matched")
	status, again := send(2)
	if status != 200 || again.ProcessingStatus != "credited" || again.ReceiptStatus != "duplicate" || again.ReceivedAtMs != first.ReceivedAtMs {
		t.Fatalf("credited TLS receipt: %d %+v", status, again)
	}
	f.assertIncome(t, 1)
}

func TestMatchOtherProviderClaimCannotBecomeCandidate(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	other := *f
	other.channel = model.Channel{Name: "other-provider", Plugin: "epay", Status: 1}
	if err := f.db.Create(&other.channel).Error; err != nil {
		t.Fatal(err)
	}
	exec(t, f.db, `INSERT INTO observer_channel_aliases VALUES (?,'test','fixture-receiver-ali','alipay')`, other.channel.ID)
	other.enrollment.Provider = "alipay"
	other.order(t, "other-provider-order", pairingNow)
	id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
	f.process(t, id, pairingNow+1000, "unmatched")
	f.assertIncome(t, 0)
}

func TestMatchWorkerContinuesPastOneFailedEventAndRetriesIt(t *testing.T) {
	now := time.Now().UnixMilli()
	f := newMatchFixture(t, "wxpay", now)
	f.order(t, "first-fails", now)
	secondOrder := f.order(t, "second-succeeds", now, "10.01")
	first, _ := f.receive(t, now, now, nil)
	second, _ := f.receive(t, now, now, func(e map[string]any) { e["amount_minor"] = 1001 })
	exec(t, f.db, fmt.Sprintf(`CREATE FUNCTION fail_first_match() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_pk=%d THEN RAISE EXCEPTION 'test first event failure'; END IF; RETURN NEW; END $$`, first))
	exec(t, f.db, `CREATE TRIGGER fail_first_match BEFORE INSERT ON observer_match_decisions FOR EACH ROW EXECUTE FUNCTION fail_first_match()`)
	if err := f.matcher.ProcessPending(context.Background(), 50); err == nil {
		t.Fatal("per-event failure not reported")
	}
	var paidOrder int64
	f.db.Raw(`SELECT order_id FROM observer_consumptions WHERE event_pk=?`, second).Scan(&paidOrder)
	if paidOrder != secondOrder.ID || count(t, f.db, "observer_consumptions") != 1 {
		t.Fatal("one failed event blocked unrelated candidate")
	}
	exec(t, f.db, `DROP TRIGGER fail_first_match ON observer_match_decisions`)
	// Prepare elapsed coordination backoff without changing evidence or sleeping.
	exec(t, f.db, `UPDATE observer_match_retries SET next_attempt_at_ms=? WHERE event_pk=?`, time.Now().UnixMilli()-1, first)
	if err := f.matcher.ProcessPending(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if count(t, f.db, "observer_consumptions") != 2 {
		t.Fatal("failed event not recovered")
	}
	var merchant model.Merchant
	f.db.First(&merchant, f.merchant.ID)
	if !merchant.Balance.Equal(decimal.RequireFromString("23.06")) {
		t.Fatal("recovery changed total income")
	}
}
func TestMatchWorkerCancellationReleasesBlockedReceiver(t *testing.T) {
	now := time.Now().UnixMilli()
	f := newMatchFixture(t, "wxpay", now)
	f.order(t, "lock-recovery", now)
	f.receive(t, now, now, nil)
	held := f.db.Begin()
	if held.Error != nil {
		t.Fatal(held.Error)
	}
	defer held.Rollback()
	if err := held.Exec(`SELECT 1 FROM observer_receivers WHERE environment='test' AND receiver_id=? FOR UPDATE`, f.enrollment.ReceiverID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := f.matcher.ProcessPending(ctx, 50); err == nil {
		t.Fatal("cancelled lock wait reported success")
	}
	held.Rollback()
	if err := f.matcher.ProcessPending(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	f.assertIncome(t, 1)
}

func TestMatchLockedPrefixDoesNotStarveIndependentReceiverAfterRestart(t *testing.T) {
	now := time.Now().UnixMilli()
	f := newMatchFixture(t, "wxpay", now)
	f.order(t, "blocked-wx", now)
	for i := 0; i < 4; i++ {
		f.receive(t, now, now, nil)
	}
	id := f.receiveIndependentAlipay(t, now)
	held := f.db.Begin()
	if held.Error != nil {
		t.Fatal(held.Error)
	}
	defer held.Rollback()
	if err := held.Exec(`SELECT 1 FROM observer_receivers WHERE environment='test' AND receiver_id=? FOR UPDATE`, f.enrollment.ReceiverID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.matcher.ProcessPending(context.Background(), 50); err == nil {
		t.Fatal("locked prefix did not exercise failure")
	}
	if count(t, f.db, "observer_match_retries") != 4 {
		t.Fatal("failed prefix backoff not durable")
	}
	restarted, err := observerstore.NewMatcher(f.db, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ProcessPending(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	var outcome string
	f.db.Raw(`SELECT outcome FROM observer_match_decisions WHERE event_pk=?`, id).Scan(&outcome)
	if outcome != "matched" {
		t.Fatal("independent receiver starved behind locked prefix")
	}
	f.assertIncome(t, 1)
}
func TestMatchBareCreditedInsertCannotBypassAudit(t *testing.T) {
	f := newMatchFixture(t, "wxpay")
	id, _ := f.receive(t, pairingNow, pairingNow, nil)
	err := f.db.Exec(`INSERT INTO observer_events(environment,device_id,event_id,receiver_id,binding_version,provider,amount_minor,currency,observed_at_ms,parser_version,source_class,identity_confidence,provider_transaction_id,body,body_sha256,first_key_id,first_request_id,first_received_at_ms,processing_status)
 SELECT environment,device_id,'bbbbbbbb-1111-4111-8111-111111111111',receiver_id,binding_version,provider,amount_minor,currency,observed_at_ms,parser_version,source_class,identity_confidence,provider_transaction_id,body,body_sha256,first_key_id,first_request_id,first_received_at_ms,'credited' FROM observer_events WHERE id=?`, id).Error
	if err == nil || !strings.Contains(err.Error(), "observer_settlement_incomplete") {
		t.Fatalf("bare credited insert bypassed settlement constraint: %v", err)
	}
	if count(t, f.db, "observer_events") != 1 {
		t.Fatal("invalid event committed")
	}
	f.assertIncome(t, 0)
}

func (f *matchFixture) receiveIndependentAlipay(t *testing.T, now int64) int64 {
	t.Helper()
	var actor int64
	f.db.Raw(`SELECT id FROM admins WHERE username='pairing-test'`).Scan(&actor)
	_, token, err := f.authority.CreateGrant(context.Background(), "fixture-receiver-ali", "replace", actor, now)
	if err != nil {
		t.Fatal(err)
	}
	req := request()
	req.EnrollmentID = "22222222-1111-4111-8111-111111111111"
	ali, err := f.authority.Redeem(context.Background(), token, req, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.authority.Activate(context.Background(), token, req, now); err != nil {
		t.Fatal(err)
	}
	other := *f
	other.enrollment = ali
	other.channel = model.Channel{Name: "independent-ali", Plugin: "epay", Status: 1}
	if err := f.db.Create(&other.channel).Error; err != nil {
		t.Fatal(err)
	}
	exec(t, f.db, `INSERT INTO observer_channel_aliases VALUES (?,'test','fixture-receiver-ali','alipay')`, other.channel.ID)
	other.order(t, "independent-ali", now)
	id, _ := other.receive(t, now, now, nil)
	return id
}

func TestMatchLockedEventDoesNotStarveIndependentReceiver(t *testing.T) {
	now := time.Now().UnixMilli()
	f := newMatchFixture(t, "wxpay", now)
	f.order(t, "event-locked-wx", now)
	blocked, _ := f.receive(t, now, now, nil)
	id := f.receiveIndependentAlipay(t, now)
	held := f.db.Begin()
	if held.Error != nil {
		t.Fatal(held.Error)
	}
	defer held.Rollback()
	if err := held.Exec(`SELECT 1 FROM observer_events WHERE id=? FOR UPDATE`, blocked).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.matcher.ProcessPending(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	var outcome string
	f.db.Raw(`SELECT outcome FROM observer_match_decisions WHERE event_pk=?`, id).Scan(&outcome)
	if outcome != "matched" {
		t.Fatal("independent receiver starved behind locked event")
	}
	f.assertIncome(t, 1)
}

func TestMatchRetryCoordinationFailureStillProcessesNextReceiver(t *testing.T) {
	now := time.Now().UnixMilli()
	f := newMatchFixture(t, "wxpay", now)
	f.order(t, "late-event-lock", now)
	blocked, _ := f.receive(t, now, now, nil)
	id := f.receiveIndependentAlipay(t, now)
	held := f.db.Begin()
	if held.Error != nil {
		t.Fatal(held.Error)
	}
	defer held.Rollback()
	var once sync.Once
	locked := false
	callback := "test:lock_event_after_candidate_scan"
	if err := f.db.Callback().Row().After("gorm:row").Register(callback, func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		if strings.Contains(query, "SELECT * FROM observer_events WHERE id=") && !strings.Contains(query, "FOR UPDATE") {
			once.Do(func() {
				if err := held.Exec(`SELECT 1 FROM observer_events WHERE id=? FOR UPDATE`, blocked).Error; err != nil {
					t.Fatal(err)
				}
				locked = true
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Row().Remove(callback)
	if err := f.matcher.ProcessPending(context.Background(), 50); err == nil || !locked {
		t.Fatal("late event lock did not exercise coordination failure")
	}
	var outcome string
	f.db.Raw(`SELECT outcome FROM observer_match_decisions WHERE event_pk=?`, id).Scan(&outcome)
	if outcome != "matched" {
		t.Fatal("retry coordination failure truncated unrelated event")
	}
	if count(t, f.db, "observer_match_retries") != 0 {
		t.Fatal("unexpected retry persisted through held event lock")
	}
	f.assertIncome(t, 1)
}
