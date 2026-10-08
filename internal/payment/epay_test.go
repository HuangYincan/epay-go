package payment

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/example/epay-go/pkg/sign"
	"github.com/shopspring/decimal"
)

func TestEPayAdapterReturnsConfiguredPersonalQR(t *testing.T) {
	a, err := NewEPayAdapter([]byte(`{"wechat_qr":"wxp://pay","alipay_qr":"https://qr.alipay.com/x","notify_key":"observer-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.CreateOrder(context.Background(), &CreateOrderRequest{
		Amount: decimal.RequireFromString("2.00"),
		Extra:  map[string]string{"pay_type": "wxpay"},
	})
	if err != nil || result.PayType != "qrcode" || result.PayURL != "wxp://pay" {
		t.Fatalf("unexpected personal QR result: %#v, %v", result, err)
	}
}

func TestEPayAdapterVerifiesAmountAndReplaySafeFields(t *testing.T) {
	a, err := NewEPayAdapter([]byte(`{"wechat_qr":"wxp://pay","alipay_qr":"https://qr.alipay.com/x","notify_key":"observer-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"trade_no":     {"T1"},
		"money":        {"2.00"},
		"api_trade_no": {"P1"},
		"trade_status": {"TRADE_SUCCESS"},
		"sign_type":    {"MD5"},
	}
	values.Set("sign", sign.GenerateMD5Sign(values, "observer-secret"))
	req := httptest.NewRequest("POST", "/api/pay/notify/epay/1", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result, err := a.ParseNotify(context.Background(), req)
	if err != nil || result.Status != "success" || !result.Amount.Equal(decimal.RequireFromString("2.00")) {
		t.Fatalf("unexpected callback result: %#v, %v", result, err)
	}
	values.Set("money", "3.00")
	req = httptest.NewRequest("POST", "/api/pay/notify/epay/1", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err = a.ParseNotify(context.Background(), req); err == nil {
		t.Fatal("tampered amount was accepted")
	}
}
