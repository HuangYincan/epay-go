package service

import (
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Every test uses its own schema in a disposable PostgreSQL database. The real
// PostgreSQL driver is necessary here: mocks cannot prove row-lock semantics.
func paymentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("EPAY_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set EPAY_TEST_DATABASE_DSN to run PostgreSQL payment integration tests")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("epay_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *sql.DB
	previous := database.DB
	t.Cleanup(func() {
		database.DB = previous
		if pool != nil {
			pool.Close()
		}
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	config.RuntimeParams["search_path"] = schema
	config.RuntimeParams["statement_timeout"] = "10000"
	pool = stdlib.OpenDB(*config)
	pool.SetMaxOpenConns(40)
	pool.SetMaxIdleConns(40)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	if err := db.AutoMigrate(&model.Merchant{}, &model.Channel{}, &model.Order{}, &model.BalanceRecord{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func paymentFixture(t *testing.T, db *gorm.DB, orders int) (model.Merchant, []model.Order) {
	t.Helper()
	merchant := model.Merchant{
		Username: "test-merchant", Password: "unused", ApiKey: "test-key",
		Balance: decimal.RequireFromString("20.01"),
	}
	if err := db.Create(&merchant).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{Name: "test-channel", Plugin: "wechat"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	result := make([]model.Order, orders)
	for i := range result {
		result[i] = model.Order{
			TradeNo: fmt.Sprintf("test-order-%d", i), OutTradeNo: fmt.Sprintf("merchant-order-%d", i),
			MerchantID: merchant.ID, ChannelID: channel.ID, PayType: "wxpay",
			Amount: decimal.RequireFromString("10.01"), RealAmount: decimal.RequireFromString("10.01"),
			Fee: decimal.RequireFromString("0.03"), Status: model.OrderStatusUnpaid,
		}
		if err := db.Create(&result[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return merchant, result
}

func paymentState(t *testing.T, db *gorm.DB, merchantID int64, tradeNo string) (model.Merchant, model.Order, []model.BalanceRecord) {
	t.Helper()
	var merchant model.Merchant
	var order model.Order
	var records []model.BalanceRecord
	if err := db.First(&merchant, merchantID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("merchant_id = ?", merchantID).Order("id").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	return merchant, order, records
}

// Hold callers that have read the same old state long enough to expose races.
// With database locks, only the first caller can observe that state; its bounded
// wait then expires and subsequent callers see the committed state.
func synchronizePaymentReads(t *testing.T, db *gorm.DB, callers int, matches func(*gorm.DB) bool) {
	t.Helper()
	var arrivals atomic.Int32
	gate := make(chan struct{})
	const name = "test:synchronize_payment_reads"
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Error != nil || !matches(tx) {
			return
		}
		if arrivals.Add(1) == int32(callers) {
			close(gate)
		}
		select {
		case <-gate:
		case <-time.After(200 * time.Millisecond):
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
}

func concurrentPaymentCalls(t *testing.T, callers int, call func(int) error) {
	t.Helper()
	start := make(chan struct{})
	errors := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			<-start
			errors <- call(i)
		}(i)
	}
	close(start)
	for i := 0; i < callers; i++ {
		if err := <-errors; err != nil {
			t.Errorf("payment call failed: %v", err)
		}
	}
}

func TestProcessPayNotifyConcurrentDuplicate(t *testing.T) {
	db := paymentTestDB(t)
	merchant, orders := paymentFixture(t, db, 1)
	const callers = 32
	synchronizePaymentReads(t, db, callers, func(tx *gorm.DB) bool {
		order, ok := tx.Statement.Dest.(*model.Order)
		return ok && order.Status == model.OrderStatusUnpaid
	})
	concurrentPaymentCalls(t, callers, func(_ int) error {
		// Separate services represent the webhook and active-query worker, or
		// multiple application instances using the same database.
		return NewOrderService().ProcessPayNotify(orders[0].TradeNo, "provider-1", "buyer", orders[0].Amount)
	})
	gotMerchant, gotOrder, records := paymentState(t, db, merchant.ID, orders[0].TradeNo)
	want := merchant.Balance.Add(orders[0].Amount.Sub(orders[0].Fee))
	if !gotMerchant.Balance.Equal(want) || len(records) != 1 {
		t.Fatalf("duplicate payment credited more than once: balance=%s want=%s records=%d want=1", gotMerchant.Balance, want, len(records))
	}
	if gotOrder.Status != model.OrderStatusPaid || gotOrder.PaidAt == nil || gotOrder.ApiTradeNo != "provider-1" {
		t.Fatalf("payment metadata not committed: %+v", gotOrder)
	}
	if !records[0].BeforeBalance.Equal(merchant.Balance) || !records[0].AfterBalance.Equal(want) {
		t.Fatalf("incorrect ledger balances: %+v", records[0])
	}
}

func TestProcessPayNotifyConcurrentMerchantOrders(t *testing.T) {
	db := paymentTestDB(t)
	const callers = 12
	merchant, orders := paymentFixture(t, db, callers)
	synchronizePaymentReads(t, db, callers, func(tx *gorm.DB) bool {
		m, ok := tx.Statement.Dest.(*model.Merchant)
		return ok && m.Balance.Equal(merchant.Balance)
	})
	concurrentPaymentCalls(t, callers, func(i int) error {
		return NewOrderService().ProcessPayNotify(orders[i].TradeNo, fmt.Sprintf("provider-%d", i), "buyer", orders[i].Amount)
	})
	gotMerchant, _, records := paymentState(t, db, merchant.ID, orders[0].TradeNo)
	income := orders[0].Amount.Sub(orders[0].Fee)
	want := merchant.Balance.Add(income.Mul(decimal.NewFromInt(callers)))
	if !gotMerchant.Balance.Equal(want) || len(records) != callers {
		t.Fatalf("lost payment: balance=%s want=%s records=%d want=%d", gotMerchant.Balance, want, len(records), callers)
	}
	balance := merchant.Balance
	for _, record := range records {
		if !record.BeforeBalance.Equal(balance) || !record.AfterBalance.Equal(balance.Add(income)) {
			t.Fatalf("ledger chain is broken: before=%s after=%s expected before=%s", record.BeforeBalance, record.AfterBalance, balance)
		}
		balance = record.AfterBalance
	}
}

func TestProcessPayNotifyRollback(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit_failure_%t", deferred), func(t *testing.T) {
			db := paymentTestDB(t)
			merchant, orders := paymentFixture(t, db, 1)
			if err := db.Exec(`CREATE FUNCTION reject_payment_record() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'injected ledger failure'; END $$`).Error; err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER reject_payment_record BEFORE INSERT ON balance_records
				FOR EACH ROW EXECUTE FUNCTION reject_payment_record()`
			if deferred {
				trigger = `CREATE CONSTRAINT TRIGGER reject_payment_record AFTER INSERT ON balance_records
					DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_payment_record()`
			}
			if err := db.Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			service := NewOrderService()
			if err := service.ProcessPayNotify(orders[0].TradeNo, "provider-1", "buyer", orders[0].Amount); err == nil {
				t.Error("ledger failure was acknowledged as a successful payment")
			}
			gotMerchant, gotOrder, records := paymentState(t, db, merchant.ID, orders[0].TradeNo)
			if gotOrder.Status != model.OrderStatusUnpaid || gotOrder.PaidAt != nil || gotOrder.ApiTradeNo != "" || !gotMerchant.Balance.Equal(merchant.Balance) || len(records) != 0 {
				t.Fatalf("failed payment left partial writes: status=%d provider=%q balance=%s records=%d", gotOrder.Status, gotOrder.ApiTradeNo, gotMerchant.Balance, len(records))
			}
			if err := db.Exec("DROP TRIGGER reject_payment_record ON balance_records").Error; err != nil {
				t.Fatal(err)
			}
			if err := service.ProcessPayNotify(orders[0].TradeNo, "provider-1", "buyer", orders[0].Amount); err != nil {
				t.Fatal(err)
			}
			gotMerchant, gotOrder, records = paymentState(t, db, merchant.ID, orders[0].TradeNo)
			if gotOrder.Status != model.OrderStatusPaid || !gotMerchant.Balance.Equal(merchant.Balance.Add(orders[0].Amount.Sub(orders[0].Fee))) || len(records) != 1 {
				t.Fatal("retry after rollback did not credit exactly once")
			}
		})
	}
}

func TestProcessPayNotifyAmountAndReplay(t *testing.T) {
	db := paymentTestDB(t)
	merchant, orders := paymentFixture(t, db, 1)
	service := NewOrderService()
	order := orders[0]
	if err := service.ProcessPayNotify(order.TradeNo, "provider-1", "buyer", decimal.NewFromInt(1)); err == nil {
		t.Fatal("incorrect amount accepted")
	}
	gotMerchant, gotOrder, records := paymentState(t, db, merchant.ID, order.TradeNo)
	if gotOrder.Status != model.OrderStatusUnpaid || !gotMerchant.Balance.Equal(merchant.Balance) || len(records) != 0 {
		t.Fatal("incorrect amount changed payment state")
	}
	if err := service.ProcessPayNotify(order.TradeNo, "provider-1", "buyer", order.Amount); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessPayNotify(order.TradeNo, "provider-1", "buyer", order.Amount); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessPayNotify(order.TradeNo, "provider-1", "buyer", decimal.NewFromInt(1)); err == nil {
		t.Fatal("incorrect amount accepted on replay")
	}
	gotMerchant, gotOrder, records = paymentState(t, db, merchant.ID, order.TradeNo)
	if gotOrder.Status != model.OrderStatusPaid || !gotMerchant.Balance.Equal(merchant.Balance.Add(order.Amount.Sub(order.Fee))) || len(records) != 1 {
		t.Fatal("replay changed payment state")
	}
}
