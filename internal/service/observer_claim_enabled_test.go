//go:build observersynthetic

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestSyntheticConcurrentAmountClaimsAcrossAliasesAndMerchants(t *testing.T) {
	f := newClaimFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.service.Create(context.Background(), f.request(fmt.Sprintf("concurrent-%d", i), "wxpay", i%2, (i/2)%2))
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, observerstore.ErrAmountUnavailable) {
			t.Fatalf("unexpected failure %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("%d concurrent windows", success)
	}
	f.counts(t, 1, 1)
	var order model.Order
	if err := f.db.First(&order).Error; err != nil {
		t.Fatal(err)
	}
	if !order.Amount.Equal(decimal.RequireFromString("10.01")) || !order.RealAmount.Equal(order.Amount) || order.ObserverPayExpiresAt == nil || !order.ObserverPayExpiresAt.Equal(order.CreatedAt.Add(10*time.Minute)) || order.NextQueryAt != nil {
		t.Fatal("order amount/window changed")
	}
	// Same amount on the other provider has an independent scope.
	f.create(t, "alipay-independent", "alipay")
	f.counts(t, 2, 2)
	if err := f.service.ProcessPayNotify(order.TradeNo, "not-a-real-provider-id", "", order.Amount, order.ChannelID); !errors.Is(err, observerstore.ErrObserverSettlement) {
		t.Fatalf("legacy settlement accepted: %v", err)
	}
	var records int64
	f.db.Table("balance_records").Count(&records)
	if records != 0 {
		t.Fatal("legacy callback credited observer order")
	}
}

func TestSyntheticIdempotentOrderDoesNotRenewOrChangeAmount(t *testing.T) {
	f := newClaimFixture(t)
	o := f.create(t, "idempotent", "wxpay")
	f.now = f.now.Add(time.Minute)
	result, err := f.service.Create(context.Background(), f.request("idempotent", "wxpay", 1, 0))
	if err == nil || result != nil {
		t.Fatal("explicit channel change accepted")
	}
	req := f.request("idempotent", "wxpay", 0, 0)
	result, err = f.service.Create(context.Background(), req)
	if err != nil || result.TradeNo != o.TradeNo {
		t.Fatalf("idempotent retry: %v", err)
	}
	stored, _ := f.service.GetByTradeNo(o.TradeNo)
	if !stored.ObserverPayExpiresAt.Equal(*o.ObserverPayExpiresAt) {
		t.Fatal("window renewed")
	}
	req.Amount = decimal.RequireFromString("10.02")
	if _, err := f.service.Create(context.Background(), req); err == nil {
		t.Fatal("changed amount accepted")
	}
	f.counts(t, 1, 1)
}

func TestSyntheticClaimsSurviveExpirySettlementAndBindingReplacement(t *testing.T) {
	for _, state := range []string{"expired", "paid", "refund", "binding", "channel-archive"} {
		t.Run(state, func(t *testing.T) {
			f := newClaimFixture(t)
			o := f.create(t, "original", "wxpay")
			switch state {
			case "expired":
				f.now = f.now.Add(24 * time.Hour)
			case "paid":
				f.exec(t, `UPDATE orders SET status=1 WHERE id=?`, o.ID)
			case "refund":
				f.exec(t, `UPDATE orders SET status=2 WHERE id=?`, o.ID)
			case "binding":
				f.exec(t, `INSERT INTO observer_devices VALUES ('claim-test','old-device',true),('claim-test','new-device',true)`)
				f.exec(t, `INSERT INTO observer_bindings VALUES ('claim-test','receiver-wx',1,'old-device','wxpay',true)`)
				f.exec(t, `UPDATE observer_bindings SET active=false WHERE environment='claim-test' AND receiver_id='receiver-wx'`)
				f.exec(t, `INSERT INTO observer_bindings VALUES ('claim-test','receiver-wx',2,'new-device','wxpay',true)`)
			case "channel-archive":
				f.exec(t, `UPDATE channels SET deleted_at=now() WHERE id=?`, f.channels[0].ID)
			}
			_, err := f.service.Create(context.Background(), f.request("reuse", "wxpay", 1, 1))
			if !errors.Is(err, observerstore.ErrAmountUnavailable) {
				t.Fatalf("reused amount after %s: %v", state, err)
			}
			f.counts(t, 1, 1)
		})
	}
}

func TestSyntheticCheckoutExpiresEvenWithCachedQR(t *testing.T) {
	f := newClaimFixture(t)
	o := f.create(t, "checkout", "wxpay")
	for _, offset := range []time.Duration{-time.Millisecond, 10 * time.Minute, 11 * time.Minute} {
		f.now = o.CreatedAt.Add(offset)
		if r, err := f.service.Checkout(context.Background(), o.TradeNo, "wxpay"); !errors.Is(err, observerstore.ErrPaymentWindow) || r != nil {
			t.Fatalf("window offset %s: %v", offset, err)
		}
	}
	f.now = o.CreatedAt.Add(10*time.Minute - time.Millisecond)
	if r, err := f.service.Checkout(context.Background(), o.TradeNo, "wxpay"); err != nil || r.PayURL != "synthetic://wxpay" {
		t.Fatalf("last window millisecond: %v", err)
	}
	f.exec(t, `UPDATE observer_receivers SET enabled=false WHERE receiver_id='receiver-wx'`)
	if _, err := f.service.Checkout(context.Background(), o.TradeNo, "wxpay"); !errors.Is(err, observerstore.ErrSyntheticDisabled) {
		t.Fatalf("disabled receiver returned QR: %v", err)
	}
}

func TestSyntheticOrderScopeMustBeExplicitAndEnabled(t *testing.T) {
	for _, condition := range []string{"no-env", "wrong-env", "environment", "receiver", "merchant", "channel"} {
		t.Run(condition, func(t *testing.T) {
			f := newClaimFixture(t)
			switch condition {
			case "no-env":
				t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "")
			case "wrong-env":
				t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "another-env")
			case "environment":
				f.exec(t, `UPDATE observer_environments SET enabled=false`)
			case "receiver":
				f.exec(t, `UPDATE observer_receivers SET enabled=false`)
			case "merchant":
				f.exec(t, `UPDATE observer_synthetic_merchants SET enabled=false`)
			case "channel":
				f.exec(t, `UPDATE channels SET status=0 WHERE id=?`, f.channels[0].ID)
			}
			if _, err := f.service.Create(context.Background(), f.request("scope", "wxpay", 0, 0)); err == nil {
				t.Fatal("disabled scope created order")
			}
			f.counts(t, 0, 0)
		})
	}
}

func TestSyntheticClaimCommitFailureRollsBackAndRetryWorks(t *testing.T) {
	f := newClaimFixture(t)
	f.exec(t, `CREATE FUNCTION fail_claim_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test_commit_failure'; END $$;
 CREATE CONSTRAINT TRIGGER test_fail_commit AFTER INSERT ON observer_amount_claims DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_claim_commit()`)
	if _, err := f.service.Create(context.Background(), f.request("rollback", "wxpay", 0, 0)); err == nil {
		t.Fatal("commit failure reported success")
	}
	f.counts(t, 0, 0)
	f.exec(t, `DROP TRIGGER test_fail_commit ON observer_amount_claims`)
	f.create(t, "rollback", "wxpay")
	f.counts(t, 1, 1)
}

func TestSyntheticDatabaseRetainsClaimsAndRejectsOrphanWindows(t *testing.T) {
	f := newClaimFixture(t)
	o := f.create(t, "immutable", "wxpay")
	for _, q := range []string{`DELETE FROM observer_amount_claims`, `UPDATE observer_amount_claims SET amount_minor=1002`, `UPDATE observer_amount_claims SET observer_pay_expires_at_ms=observer_pay_expires_at_ms+1`, `UPDATE orders SET amount=10.02`, `UPDATE orders SET real_amount=10.02`, `UPDATE orders SET observer_pay_expires_at=observer_pay_expires_at+interval '1 minute'`, `DELETE FROM observer_synthetic_merchants`, `UPDATE observer_synthetic_merchants SET environment='other'`} {
		if err := f.db.Exec(q).Error; err == nil {
			t.Fatalf("history mutation accepted: %s", q)
		}
	}
	orphan := model.Order{TradeNo: "orphan", OutTradeNo: "orphan", MerchantID: f.merchants[0].ID, ChannelID: f.channels[0].ID, PayType: "wxpay", Amount: decimal.NewFromInt(20), RealAmount: decimal.NewFromInt(20), ObserverPayExpiresAt: o.ObserverPayExpiresAt}
	if err := f.db.Create(&orphan).Error; err == nil {
		t.Fatal("unclaimed window committed")
	}
	f.counts(t, 1, 1)
}

func TestSyntheticLegacyHistoryNeverGetsReusedOrUpgraded(t *testing.T) {
	f := newClaimFixture(t)
	legacyChannel := f.channels[0]
	legacyChannel.ID = 0
	legacyChannel.Name = "legacy-history"
	if err := f.db.Create(&legacyChannel).Error; err != nil {
		t.Fatal(err)
	}
	legacy := model.Order{TradeNo: "legacy", OutTradeNo: "legacy", MerchantID: f.merchants[0].ID, ChannelID: legacyChannel.ID, PayType: "wxpay", Amount: decimal.RequireFromString("10.01"), RealAmount: decimal.RequireFromString("10.01")}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO observer_channel_aliases VALUES (?,'claim-test','receiver-wx','wxpay')`, legacyChannel.ID)
	if _, err := f.service.Create(context.Background(), f.request("new", "wxpay", 0, 0)); !errors.Is(err, observerstore.ErrAmountUnavailable) {
		t.Fatalf("legacy amount reused: %v", err)
	}
	if _, err := f.service.Checkout(context.Background(), legacy.TradeNo, "wxpay"); err == nil {
		t.Fatal("legacy checkout upgraded")
	}
	if err := f.service.ProcessPayNotify(legacy.TradeNo, "synthetic-negative", "", legacy.Amount); !errors.Is(err, observerstore.ErrObserverSettlement) {
		t.Fatal("legacy callback upgraded history")
	}
	f.counts(t, 1, 0)
}

func TestSyntheticQRProviderComesFromOriginalOrder(t *testing.T) {
	f := newClaimFixture(t)
	req := f.request("extra-provider", "wxpay", 0, 0)
	req.Extra["pay_type"] = "alipay"
	r, err := f.service.Create(context.Background(), req)
	if err != nil || r.PayURL != "synthetic://wxpay" {
		t.Fatalf("caller extra changed payment provider: %v", err)
	}
}

func TestSyntheticConcurrentIdenticalRequestReturnsOneWindow(t *testing.T) {
	f := newClaimFixture(t)
	var wg sync.WaitGroup
	results := make(chan *CreateOrderResponse, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.service.Create(context.Background(), f.request("same-request", "wxpay", 0, 0))
			results <- r
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for r := range results {
		if first == "" {
			first = r.TradeNo
		}
		if r.TradeNo != first {
			t.Fatal("idempotent request created different orders")
		}
	}
	f.counts(t, 1, 1)
}

func TestSyntheticCachedQRRejectsDisabledOrArchivedOriginalChannel(t *testing.T) {
	for _, change := range []string{"status=0", "deleted_at=now()"} {
		t.Run(change, func(t *testing.T) {
			f := newClaimFixture(t)
			o := f.create(t, "cached-channel", "wxpay")
			f.exec(t, "UPDATE channels SET "+change+" WHERE id=?", o.ChannelID)
			if r, err := f.service.Checkout(context.Background(), o.TradeNo, "wxpay"); !errors.Is(err, observerstore.ErrSyntheticDisabled) || r != nil {
				t.Fatalf("disabled original channel returned cached QR: %v", err)
			}
		})
	}
}

func TestSyntheticAliasRegistrationCannotIntroduceClaimHistoryCollision(t *testing.T) {
	for attempt := 0; attempt < 4; attempt++ {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			f := newClaimFixture(t)
			legacyChannel := f.channels[0]
			legacyChannel.ID = 0
			legacyChannel.Name = "unmapped-history"
			if err := f.db.Create(&legacyChannel).Error; err != nil {
				t.Fatal(err)
			}
			legacy := model.Order{TradeNo: "legacy-before-alias", OutTradeNo: "legacy-before-alias", MerchantID: f.merchants[1].ID, ChannelID: legacyChannel.ID, PayType: "wxpay", Amount: decimal.RequireFromString("10.01"), RealAmount: decimal.RequireFromString("10.01")}
			if err := f.db.Create(&legacy).Error; err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			claimed := make(chan error, 1)
			aliased := make(chan error, 1)
			go func() {
				<-start
				_, err := f.service.Create(context.Background(), f.request("race-claim", "wxpay", 0, 0))
				claimed <- err
			}()
			go func() {
				<-start
				aliased <- f.db.Exec(`INSERT INTO observer_channel_aliases VALUES (?,'claim-test','receiver-wx','wxpay')`, legacyChannel.ID).Error
			}()
			close(start)
			claimErr, aliasErr := <-claimed, <-aliased
			if (claimErr == nil) == (aliasErr == nil) {
				t.Fatalf("must choose one safe outcome, claim=%v alias=%v", claimErr, aliasErr)
			}
			if claimErr == nil {
				f.counts(t, 2, 1)
			} else {
				if !errors.Is(claimErr, observerstore.ErrAmountUnavailable) {
					t.Fatal(claimErr)
				}
				f.counts(t, 1, 0)
			}
		})
	}
}

func TestSyntheticArchivedCandidateCannotFallBackToLegacyChannel(t *testing.T) {
	f := newClaimFixture(t)
	f.exec(t, `UPDATE channels SET status=0 WHERE id=?`, f.channels[1].ID)
	legacy := f.channels[0]
	legacy.ID = 0
	legacy.Name = "legacy-fallback"
	legacy.Sort = 100
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	archived := false
	callback := "test:archive_observer_after_candidate_read"
	if err := f.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.Channel); ok && tx.Statement.Table == "channels" {
			once.Do(func() {
				f.exec(t, `UPDATE channels SET deleted_at=now() WHERE id=?`, f.channels[0].ID)
				archived = true
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Query().Remove(callback)
	req := f.request("no-fallback", "wxpay", 0, 0)
	req.ChannelID = 0
	r, err := f.service.Create(context.Background(), req)
	if !archived || r != nil || !errors.Is(err, observerstore.ErrSyntheticDisabled) {
		t.Fatalf("archived candidate fallback: archived=%v response=%v error=%v", archived, r, err)
	}
	f.counts(t, 0, 0)
}

func TestSyntheticCachedQRRejectsDisabledMerchant(t *testing.T) {
	f := newClaimFixture(t)
	o := f.create(t, "disabled-merchant", "wxpay")
	f.exec(t, `UPDATE merchants SET status=0 WHERE id=?`, o.MerchantID)
	if r, err := f.service.Checkout(context.Background(), o.TradeNo, "wxpay"); r != nil || !errors.Is(err, observerstore.ErrSyntheticDisabled) {
		t.Fatalf("disabled merchant cached QR: %v", err)
	}
}
