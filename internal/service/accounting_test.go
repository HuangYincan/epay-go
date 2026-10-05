package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/payment"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type ledgerAdapter struct {
	refundStatus string
	queryStatus  string
	refundError  error
	queryAmount  *decimal.Decimal
	calls        atomic.Int32
	createCalls  atomic.Int32
}

func (a *ledgerAdapter) CreateOrder(_ context.Context, r *payment.CreateOrderRequest) (*payment.CreateOrderResponse, error) {
	a.createCalls.Add(1)
	return &payment.CreateOrderResponse{PayType: "qrcode", PayURL: "weixin://test"}, nil
}
func (a *ledgerAdapter) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, errors.New("unused")
}
func (a *ledgerAdapter) Refund(context.Context, *payment.RefundRequest) (*payment.RefundResponse, error) {
	a.calls.Add(1)
	return &payment.RefundResponse{Status: a.refundStatus}, a.refundError
}
func (a *ledgerAdapter) QueryRefund(_ context.Context, r *payment.RefundRequest) (*payment.RefundResponse, error) {
	amount := r.Amount
	if a.queryAmount != nil {
		amount = *a.queryAmount
	}
	return &payment.RefundResponse{Status: a.queryStatus, Amount: amount}, nil
}
func (a *ledgerAdapter) ParseNotify(context.Context, *http.Request) (*payment.NotifyResult, error) {
	return nil, errors.New("unused")
}
func (a *ledgerAdapter) NotifySuccess() string { return "success" }
func refundFixture(t *testing.T, status string) (*gorm.DB, model.Merchant, model.Order, *model.Refund, *ledgerAdapter) {
	db := paymentTestDB(t)
	if err := db.AutoMigrate(&model.Refund{}); err != nil {
		t.Fatal(err)
	}
	m, orders := paymentFixture(t, db, 1)
	o := orders[0]
	if err := db.Model(&o).Update("status", model.OrderStatusPaid).Error; err != nil {
		t.Fatal(err)
	}
	a := &ledgerAdapter{refundStatus: status, queryStatus: "success"}
	payment.Register("ledger-test", func(json.RawMessage) (payment.PaymentAdapter, error) { return a, nil })
	if err := db.Model(&model.Channel{}).Where("id = ?", o.ChannelID).Update("plugin", "ledger-test").Error; err != nil {
		t.Fatal(err)
	}
	r, err := NewRefundService().CreateRefund(m.ID, &CreateRefundRequest{TradeNo: o.TradeNo, Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	return db, m, o, r, a
}
func settlementFixture(t *testing.T) (*gorm.DB, model.Merchant, *SettlementService) {
	db := paymentTestDB(t)
	if err := db.AutoMigrate(&model.Settlement{}); err != nil {
		t.Fatal(err)
	}
	m, _ := paymentFixture(t, db, 0)
	return db, m, NewSettlementService()
}
func settlementRequest(id int64) *ApplyRequest {
	return &ApplyRequest{MerchantID: id, Amount: decimal.NewFromInt(20), AccountType: "alipay", AccountNo: "test", AccountName: "test"}
}
func TestSettlementConcurrentApply(t *testing.T) {
	db, m, s := settlementFixture(t)
	synchronizePaymentReads(t, db, 4, func(tx *gorm.DB) bool { _, ok := tx.Statement.Dest.(*model.Merchant); return ok })
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Apply(settlementRequest(m.ID)); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d settlements", accepted.Load())
	}
	db.First(&m, m.ID)
	if !m.Balance.Equal(decimal.RequireFromString("0.01")) || !m.FrozenBalance.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("wrong funds: %+v", m)
	}
	var records []model.BalanceRecord
	db.Find(&records)
	if len(records) != 1 || !records[0].BeforeBalance.Equal(decimal.RequireFromString("20.01")) {
		t.Fatal("incorrect freeze ledger")
	}
}
func TestSettlementCommitFailureAndRetry(t *testing.T) {
	for _, op := range []string{"apply", "reject", "complete"} {
		t.Run(op, func(t *testing.T) {
			db, m, s := settlementFixture(t)
			var v *model.Settlement
			var err error
			if op != "apply" {
				v, err = s.Apply(settlementRequest(m.ID))
				if err != nil {
					t.Fatal(err)
				}
				if op == "complete" {
					if err = s.Approve(v.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = db.Exec(`CREATE FUNCTION reject_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'commit failed'; END $$`).Error; err != nil {
				t.Fatal(err)
			}
			if err = db.Exec(`CREATE CONSTRAINT TRIGGER reject_record AFTER INSERT ON balance_records DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_record()`).Error; err != nil {
				t.Fatal(err)
			}
			switch op {
			case "apply":
				_, err = s.Apply(settlementRequest(m.ID))
			case "reject":
				err = s.Reject(v.ID, "test")
			case "complete":
				err = s.Complete(v.ID)
			}
			if err == nil {
				t.Fatal("commit failure hidden")
			}
			db.First(&m, m.ID)
			if op == "apply" {
				var n int64
				db.Model(&model.Settlement{}).Count(&n)
				if n != 0 || !m.Balance.Equal(decimal.RequireFromString("20.01")) {
					t.Fatal("apply partially committed")
				}
			} else {
				db.First(v, v.ID)
				want := int8(model.SettleStatusPending)
				if op == "complete" {
					want = model.SettleStatusProcessing
				}
				if v.Status != want || !m.FrozenBalance.Equal(decimal.NewFromInt(20)) {
					t.Fatal("terminal state escaped rollback")
				}
			}
			db.Exec("DROP TRIGGER reject_record ON balance_records")
			if op == "complete" {
				concurrentPaymentCalls(t, 4, func(int) error { return s.Complete(v.ID) })
				db.First(&m, m.ID)
				if !m.FrozenBalance.IsZero() {
					t.Fatal("duplicate completion deducted frozen funds")
				}
			}
			if op == "reject" {
				concurrentPaymentCalls(t, 4, func(int) error { return s.Reject(v.ID, "test") })
				db.First(&m, m.ID)
				if !m.FrozenBalance.IsZero() || !m.Balance.Equal(decimal.RequireFromString("20.01")) {
					t.Fatal("duplicate rejection credited twice")
				}
			}
		})
	}
}
func TestProfileUpdatePreservesPayment(t *testing.T) {
	db, m, orders := func() (*gorm.DB, model.Merchant, []model.Order) {
		db := paymentTestDB(t)
		m, o := paymentFixture(t, db, 1)
		return db, m, o
	}()
	var fired atomic.Bool
	db.Callback().Query().After("gorm:query").Register("test:credit", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*model.Merchant); ok && fired.CompareAndSwap(false, true) {
			if err := NewOrderService().ProcessPayNotify(orders[0].TradeNo, "provider", "", orders[0].Amount); err != nil {
				t.Error(err)
			}
		}
	})
	defer db.Callback().Query().Remove("test:credit")
	if _, err := NewMerchantService().ResetAPIKey(m.ID); err != nil {
		t.Fatal(err)
	}
	got, _, records := paymentState(t, db, m.ID, orders[0].TradeNo)
	if !got.Balance.Equal(decimal.RequireFromString("29.99")) || len(records) != 1 {
		t.Fatal("profile write overwrote payment")
	}
}
func TestRefundConcurrentAndPartial(t *testing.T) {
	db, m, o, r, a := refundFixture(t, "success")
	s := NewRefundService()
	concurrentPaymentCalls(t, 8, func(int) error { return s.ProcessRefund(r.RefundNo, true, "") })
	got, order, records := paymentState(t, db, m.ID, o.TradeNo)
	if a.calls.Load() != 1 || !got.Balance.Equal(m.Balance.Sub(decimal.NewFromInt(1))) || !got.FrozenBalance.IsZero() || order.Status != model.OrderStatusPaid || len(records) != 2 {
		t.Fatalf("refund not idempotent: calls=%d balance=%s frozen=%s status=%d records=%d", a.calls.Load(), got.Balance, got.FrozenBalance, order.Status, len(records))
	}
	remaining := o.Amount.Sub(decimal.NewFromInt(1))
	next, err := s.CreateRefund(m.ID, &CreateRefundRequest{TradeNo: o.TradeNo, Amount: remaining.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessRefund(next.RefundNo, true, ""); err != nil {
		t.Fatal(err)
	}
	got, order, _ = paymentState(t, db, m.ID, o.TradeNo)
	if order.Status != model.OrderStatusRefund || !got.Balance.Equal(m.Balance.Sub(o.Amount)) {
		t.Fatal("partial refunds did not close only at full amount")
	}
}
func TestRefundProcessingRecovery(t *testing.T) {
	db, m, o, r, a := refundFixture(t, "processing")
	s := NewRefundService()
	if err := s.ProcessRefund(r.RefundNo, true, ""); err != nil {
		t.Fatal(err)
	}
	db.First(r, r.ID)
	got, order, _ := paymentState(t, db, m.ID, o.TradeNo)
	if r.Status != model.RefundStatusProcessing || !r.FundsReserved || order.Status != model.OrderStatusPaid || !got.FrozenBalance.Equal(decimal.NewFromInt(1)) {
		t.Fatal("processing refund treated as success")
	}
	db.Model(r).Update("next_query_at", time.Now().Add(-time.Minute))
	if err := s.Reconcile(context.Background(), r.RefundNo); err != nil {
		t.Fatal(err)
	}
	db.First(r, r.ID)
	got, _, _ = paymentState(t, db, m.ID, o.TradeNo)
	if r.Status != model.RefundStatusSuccess || r.FundsReserved || !got.FrozenBalance.IsZero() || a.calls.Load() != 1 {
		t.Fatal("recovery reissued or failed to settle refund")
	}
}
func TestRefundFinalCommitFailureRecovery(t *testing.T) {
	db, m, o, r, a := refundFixture(t, "success")
	if err := db.Exec(`CREATE FUNCTION reject_final() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type = 'refund' THEN RAISE EXCEPTION 'final commit failed'; END IF; RETURN NEW; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE CONSTRAINT TRIGGER reject_final AFTER INSERT ON balance_records DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_final()`).Error; err != nil {
		t.Fatal(err)
	}
	s := NewRefundService()
	if err := s.ProcessRefund(r.RefundNo, true, ""); err == nil {
		t.Fatal("final commit failure hidden")
	}
	db.First(r, r.ID)
	got, order, records := paymentState(t, db, m.ID, o.TradeNo)
	if r.Status != model.RefundStatusProcessing || !r.FundsReserved || order.Status != model.OrderStatusPaid || !got.FrozenBalance.Equal(decimal.NewFromInt(1)) || len(records) != 1 {
		t.Fatal("final writes escaped rollback")
	}
	db.Exec("DROP TRIGGER reject_final ON balance_records")
	db.Model(r).Update("next_query_at", time.Now().Add(-time.Minute))
	if err := s.Reconcile(context.Background(), r.RefundNo); err != nil {
		t.Fatal(err)
	}
	db.First(r, r.ID)
	got, _, _ = paymentState(t, db, m.ID, o.TradeNo)
	if r.Status != model.RefundStatusSuccess || a.calls.Load() != 1 || !got.Balance.Equal(m.Balance.Sub(decimal.NewFromInt(1))) {
		t.Fatal("recovery charged again")
	}
}
func TestRefundInvalidAndRejection(t *testing.T) {
	db, m, o, r, a := refundFixture(t, "success")
	s := NewRefundService()
	for _, amount := range []string{"-1", "0", "0.001", "100"} {
		if _, err := s.CreateRefund(m.ID, &CreateRefundRequest{TradeNo: o.TradeNo, Amount: amount}); err == nil {
			t.Fatalf("accepted %s", amount)
		}
	}
	if err := s.ProcessRefund(r.RefundNo, false, "拒绝"); err != nil {
		t.Fatal(err)
	}
	db.First(r, r.ID)
	if r.Status != model.RefundStatusFailed || a.calls.Load() != 0 {
		t.Fatal("rejected refund contacted provider")
	}
}
func orderFixture(t *testing.T, limit string) (*gorm.DB, model.Merchant, *ledgerAdapter) {
	db := paymentTestDB(t)
	m, _ := paymentFixture(t, db, 0)
	a := &ledgerAdapter{}
	payment.Register("order-test", func(json.RawMessage) (payment.PaymentAdapter, error) { return a, nil })
	c := model.Channel{Name: "order-test", Plugin: "order-test", PayTypes: "alipay", AppType: "native", Rate: decimal.RequireFromString("0.6"), DailyLimit: decimal.RequireFromString(limit), Status: 1, Sort: -1}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return db, m, a
}
func orderRequest(id int64, no string, amount int64) *CreateOrderRequest {
	return &CreateOrderRequest{MerchantID: id, OutTradeNo: no, Amount: decimal.NewFromInt(amount), Name: "test", PayType: "alipay", PayMethod: "scan"}
}
func TestOrderPercentageAndCheckout(t *testing.T) {
	db, m, a := orderFixture(t, "0")
	s := NewOrderService()
	v, err := s.Create(context.Background(), orderRequest(m.ID, "one", 100))
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.GetByTradeNo(v.TradeNo)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Fee.Equal(decimal.RequireFromString("0.60")) {
		t.Fatalf("fee=%s", o.Fee)
	}
	if _, err = s.Checkout(context.Background(), v.TradeNo, "wxpay"); err == nil {
		t.Fatal("changed original payment type")
	}
	if _, err = s.Checkout(context.Background(), v.TradeNo, "alipay"); err != nil {
		t.Fatal(err)
	}
	if a.createCalls.Load() != 1 {
		t.Fatal("checkout reissued payment")
	}
	if err = s.ProcessPayNotify(o.TradeNo, "provider", "", o.Amount, o.ChannelID+1); err == nil {
		t.Fatal("wrong callback channel accepted")
	}
	if err = s.ProcessPayNotify(o.TradeNo, "provider", "", o.Amount, o.ChannelID); err != nil {
		t.Fatal(err)
	}
	db.First(&m, m.ID)
	if !m.Balance.Equal(decimal.RequireFromString("119.41")) {
		t.Fatalf("balance=%s", m.Balance)
	}
}
func TestOrderConcurrentLimitAndNumber(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "daily_limit", true: "merchant_number"}[same], func(t *testing.T) {
			limit := "100"
			if same {
				limit = "0"
			}
			db, m, a := orderFixture(t, limit)
			var wg sync.WaitGroup
			var accepted atomic.Int32
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					no := string(rune('a' + i))
					if same {
						no = "same"
					}
					if _, err := NewOrderService().Create(context.Background(), orderRequest(m.ID, no, 60)); err == nil {
						accepted.Add(1)
					}
				}(i)
			}
			wg.Wait()
			var n int64
			db.Model(&model.Order{}).Count(&n)
			if accepted.Load() != 1 || n != 1 || a.createCalls.Load() != 1 {
				t.Fatalf("accepted=%d rows=%d provider_calls=%d", accepted.Load(), n, a.createCalls.Load())
			}
		})
	}
}
func TestOrderDisabledMethodAndPrivateNotify(t *testing.T) {
	_, m, a := orderFixture(t, "0")
	r := orderRequest(m.ID, "disabled", 1)
	r.PayMethod = "h5"
	if _, err := NewOrderService().Create(context.Background(), r); err == nil {
		t.Fatal("disabled method allowed")
	}
	r.PayMethod = "scan"
	r.MerchantNotifyURL = "http://127.0.0.1/internal"
	if _, err := NewOrderService().Create(context.Background(), r); err == nil {
		t.Fatal("private webhook accepted")
	}
	r.MerchantNotifyURL = ""
	r.Amount = decimal.RequireFromString("0.019")
	if _, err := NewOrderService().Create(context.Background(), r); err == nil {
		t.Fatal("fractional cents accepted")
	}
	if a.createCalls.Load() != 0 {
		t.Fatal("invalid order reached provider")
	}
}

func TestRefundReservationCommitFailure(t *testing.T) {
	db, m, o, r, a := refundFixture(t, "success")
	if err := db.Exec(`CREATE FUNCTION reject_freeze() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reservation commit failed'; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE CONSTRAINT TRIGGER reject_freeze AFTER INSERT ON balance_records DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_freeze()`).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewRefundService().ProcessRefund(r.RefundNo, true, ""); err == nil {
		t.Fatal("reservation commit failure hidden")
	}
	db.First(r, r.ID)
	got, _, records := paymentState(t, db, m.ID, o.TradeNo)
	if a.calls.Load() != 0 || r.Status != model.RefundStatusPending || r.FundsReserved || !got.Balance.Equal(m.Balance) || !got.FrozenBalance.IsZero() || len(records) != 0 {
		t.Fatal("failed reservation called provider or changed funds")
	}
}
func TestRefundFailureAndTimeout(t *testing.T) {
	for _, initial := range []string{"failed", "timeout"} {
		t.Run(initial, func(t *testing.T) {
			db, m, o, r, a := refundFixture(t, initial)
			s := NewRefundService()
			if initial == "timeout" {
				a.refundError = context.DeadlineExceeded
			}
			err := s.ProcessRefund(r.RefundNo, true, "")
			if initial == "timeout" {
				if err == nil {
					t.Fatal("uncertain result hidden")
				}
				db.First(r, r.ID)
				if r.Status != model.RefundStatusProcessing || !r.FundsReserved {
					t.Fatal("timeout released funds")
				}
				a.queryStatus = "failed"
				db.Model(r).Update("next_query_at", time.Now().Add(-time.Minute))
				err = s.Reconcile(context.Background(), r.RefundNo)
			}
			if err != nil {
				t.Fatal(err)
			}
			db.First(r, r.ID)
			got, _, records := paymentState(t, db, m.ID, o.TradeNo)
			if r.Status != model.RefundStatusFailed || r.FundsReserved || !got.Balance.Equal(m.Balance) || !got.FrozenBalance.IsZero() || len(records) != 2 || a.calls.Load() != 1 {
				t.Fatal("failed refund did not unfreeze exactly once")
			}
		})
	}
}
func TestRefundQueryAmountAndRetry(t *testing.T) {
	db, m, o, r, a := refundFixture(t, "processing")
	s := NewRefundService()
	if err := s.ProcessRefund(r.RefundNo, true, ""); err != nil {
		t.Fatal(err)
	}
	for _, amount := range []decimal.Decimal{decimal.Zero, decimal.NewFromInt(2)} {
		a.queryAmount = &amount
		db.Model(r).Update("next_query_at", time.Now().Add(-time.Minute))
		if err := s.Reconcile(context.Background(), r.RefundNo); err == nil {
			t.Fatal("accepted wrong reported amount")
		}
		db.First(r, r.ID)
		if r.Status != model.RefundStatusProcessing || !r.FundsReserved {
			t.Fatal("mismatch finalized funds")
		}
	}
	a.queryAmount = nil
	a.queryStatus = "not_found"
	a.refundStatus = "success"
	db.Model(r).Update("next_query_at", time.Now().Add(-time.Minute))
	if err := s.Reconcile(context.Background(), r.RefundNo); err != nil {
		t.Fatal(err)
	}
	got, _, _ := paymentState(t, db, m.ID, o.TradeNo)
	if a.calls.Load() != 2 || !got.Balance.Equal(m.Balance.Sub(decimal.NewFromInt(1))) || !got.FrozenBalance.IsZero() {
		t.Fatal("retry deducted twice")
	}
}
func TestRefundPendingAmountReservation(t *testing.T) {
	_, m, o, _, _ := refundFixture(t, "success")
	s := NewRefundService()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CreateRefund(m.ID, &CreateRefundRequest{TradeNo: o.TradeNo, Amount: "5"}); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("overcommitted pending refunds: %d", accepted.Load())
	}
}
func TestChannelPartialUpdate(t *testing.T) {
	db := paymentTestDB(t)
	c := model.Channel{Name: "config fixture", Plugin: "wechat", AppType: "native", CallbackURL: "https://example.com/callback", Status: 1, Sort: 9, Rate: decimal.RequireFromString("0.6"), DailyLimit: decimal.NewFromInt(100)}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	empty := ""
	s := NewChannelService()
	if err := s.Update(c.ID, &UpdateChannelRequest{CallbackURL: &empty}); err != nil {
		t.Fatal(err)
	}
	db.First(&c, c.ID)
	if c.CallbackURL != "" || c.Status != 1 || c.Sort != 9 || !c.Rate.Equal(decimal.RequireFromString("0.6")) || !c.DailyLimit.Equal(decimal.NewFromInt(100)) {
		t.Fatal("clear callback overwrote omitted fields")
	}
	if err := s.Update(c.ID, &UpdateChannelRequest{AppType: &empty}); err == nil {
		t.Fatal("accepted no interfaces")
	}
}
