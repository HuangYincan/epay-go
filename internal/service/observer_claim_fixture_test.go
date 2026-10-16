package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type claimFixture struct {
	db        *gorm.DB
	service   *OrderService
	merchants [2]model.Merchant
	channels  [2]model.Channel
	now       time.Time
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "claim-test")
	f := &claimFixture{db: paymentTestDB(t), service: NewOrderService(), now: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)}
	f.service.now = func() time.Time { return f.now }
	for i, name := range []string{"claim-merchant-a", "claim-merchant-b"} {
		f.merchants[i] = model.Merchant{Username: name, Password: "synthetic-only", ApiKey: "synthetic-only-" + name, Status: 1}
		if err := f.db.Create(&f.merchants[i]).Error; err != nil {
			t.Fatal(err)
		}
		f.channels[i] = model.Channel{Name: name, Plugin: "epay", AppType: "qrcode", Status: 1, Config: json.RawMessage(`{"wechat_qr":"synthetic://wxpay","alipay_qr":"synthetic://alipay","notify_key":"unused-synthetic-key"}`)}
		if err := f.db.Create(&f.channels[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.exec(t, `INSERT INTO observer_environments VALUES ('claim-test','synthetic',true)`)
	f.exec(t, `INSERT INTO observer_receivers VALUES ('claim-test','receiver-wx','wxpay','account-wx',true),('claim-test','receiver-ali','alipay','account-ali',true)`)
	for i := range f.channels {
		f.exec(t, `INSERT INTO observer_synthetic_merchants(merchant_id,environment,enabled) VALUES (?,'claim-test',true)`, f.merchants[i].ID)
		f.exec(t, `INSERT INTO observer_channel_aliases VALUES (?,'claim-test','receiver-wx','wxpay'),(?,'claim-test','receiver-ali','alipay')`, f.channels[i].ID, f.channels[i].ID)
	}
	return f
}
func (f *claimFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := f.db.Exec(q, args...).Error; err != nil {
		t.Fatal(err)
	}
}
func (f *claimFixture) request(no, provider string, channel, merchant int) *CreateOrderRequest {
	return &CreateOrderRequest{MerchantID: f.merchants[merchant].ID, ChannelID: f.channels[channel].ID, OutTradeNo: no, Amount: decimal.RequireFromString("10.01"), Name: "synthetic ordinary order", PayType: provider, PayMethod: "scan", Extra: map[string]string{"pay_type": provider}}
}
func (f *claimFixture) create(t *testing.T, no, provider string) *model.Order {
	t.Helper()
	r, err := f.service.Create(context.Background(), f.request(no, provider, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.service.GetByTradeNo(r.TradeNo)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func (f *claimFixture) counts(t *testing.T, orders, claims int64) {
	t.Helper()
	for table, want := range map[string]int64{"orders": orders, "observer_amount_claims": claims} {
		var n int64
		if err := f.db.Table(table).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("%s count=%d want=%d", table, n, want)
		}
	}
}
