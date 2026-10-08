package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/example/epay-go/pkg/sign"
	"github.com/shopspring/decimal"
)

// EPayConfig configures the deliberately small personal-payment channel. The
// QR values are the payment payloads (for example wxp://... or a hosted QR
// URL), not credentials. NotifyKey is used by a trusted QR observer to send a
// signed completion callback to /api/pay/notify/epay/:channel_id.
type EPayConfig struct {
	WechatQR  string `json:"wechat_qr"`
	AlipayQR  string `json:"alipay_qr"`
	NotifyKey string `json:"notify_key"`
}

// EPayAdapter serves a static personal QR code. It intentionally has no
// provider-side query or refund API: until a signed observer callback arrives,
// an order remains unpaid and cannot credit the New API account.
type EPayAdapter struct {
	config EPayConfig
}

func NewEPayAdapter(raw json.RawMessage) (PaymentAdapter, error) {
	var cfg EPayConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("epay 配置无效: %w", err)
	}
	cfg.WechatQR = strings.TrimSpace(cfg.WechatQR)
	cfg.AlipayQR = strings.TrimSpace(cfg.AlipayQR)
	cfg.NotifyKey = strings.TrimSpace(cfg.NotifyKey)
	if cfg.WechatQR == "" || cfg.AlipayQR == "" {
		return nil, errors.New("epay 必须同时配置微信和支付宝个人收款码")
	}
	if cfg.NotifyKey == "" {
		return nil, errors.New("epay 必须配置回调密钥")
	}
	if len(cfg.WechatQR) > 4096 || len(cfg.AlipayQR) > 4096 || len(cfg.NotifyKey) > 256 {
		return nil, errors.New("epay 配置长度无效")
	}
	return &EPayAdapter{config: cfg}, nil
}

func (e *EPayAdapter) CreateOrder(_ context.Context, req *CreateOrderRequest) (*CreateOrderResponse, error) {
	if req == nil || !req.Amount.IsPositive() || !req.Amount.Equal(req.Amount.Round(2)) {
		return nil, errors.New("支付金额无效")
	}
	payType := ""
	if req.Extra != nil {
		payType = strings.ToLower(strings.TrimSpace(req.Extra["pay_type"]))
	}
	var qr string
	switch payType {
	case "wxpay", "wechat":
		qr = e.config.WechatQR
	case "alipay", "ali":
		qr = e.config.AlipayQR
	case "":
		if e.config.WechatQR != "" && e.config.AlipayQR == "" {
			qr = e.config.WechatQR
		} else if e.config.AlipayQR != "" && e.config.WechatQR == "" {
			qr = e.config.AlipayQR
		}
	default:
		return nil, errors.New("epay 支付类型无效")
	}
	if qr == "" {
		return nil, errors.New("epay 未配置该支付类型的收款码")
	}
	return &CreateOrderResponse{PayType: "qrcode", PayURL: qr}, nil
}

func (e *EPayAdapter) QueryOrder(context.Context, string) (*QueryOrderResponse, error) {
	return nil, errors.New("个人收款码不支持主动查单")
}

func (e *EPayAdapter) Refund(context.Context, *RefundRequest) (*RefundResponse, error) {
	return nil, errors.New("个人收款码不支持自动退款")
}

// ParseNotify accepts the small signed observer callback format. All fields,
// including the amount and order number, are covered by the EPay signature.
func (e *EPayAdapter) ParseNotify(_ context.Context, r *http.Request) (*NotifyResult, error) {
	if r == nil {
		return nil, errors.New("空回调请求")
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	form := r.Form
	if signType := strings.TrimSpace(form.Get("sign_type")); signType != "" && !strings.EqualFold(signType, "MD5") {
		return nil, errors.New("epay 回调签名类型无效")
	}
	if !sign.VerifyMD5Sign(form, e.config.NotifyKey, form.Get("sign")) {
		return nil, errors.New("epay 回调签名验证失败")
	}
	tradeNo := strings.TrimSpace(form.Get("trade_no"))
	if tradeNo == "" {
		tradeNo = strings.TrimSpace(form.Get("out_trade_no"))
	}
	if tradeNo == "" || len(tradeNo) > 32 {
		return nil, errors.New("epay 回调订单号无效")
	}
	amountText := strings.TrimSpace(form.Get("money"))
	if amountText == "" {
		amountText = strings.TrimSpace(form.Get("amount"))
	}
	amount, err := decimal.NewFromString(amountText)
	if err != nil || !amount.IsPositive() || !amount.Equal(amount.Round(2)) {
		return nil, errors.New("epay 回调金额无效")
	}
	providerTradeNo := strings.TrimSpace(form.Get("api_trade_no"))
	if providerTradeNo == "" {
		providerTradeNo = strings.TrimSpace(form.Get("transaction_id"))
	}
	if providerTradeNo == "" || len(providerTradeNo) > 64 {
		return nil, errors.New("epay 回调流水号无效")
	}
	status := strings.ToUpper(strings.TrimSpace(form.Get("trade_status")))
	if status == "" {
		status = strings.ToUpper(strings.TrimSpace(form.Get("status")))
	}
	if status != "TRADE_SUCCESS" && status != "SUCCESS" && status != "PAID" {
		return &NotifyResult{TradeNo: tradeNo, ApiTradeNo: providerTradeNo, Amount: amount, Status: "fail"}, nil
	}
	return &NotifyResult{TradeNo: tradeNo, ApiTradeNo: providerTradeNo, Amount: amount, Buyer: strings.TrimSpace(form.Get("buyer")), Status: "success"}, nil
}

func (e *EPayAdapter) NotifySuccess() string { return "success" }

func init() { Register("epay", NewEPayAdapter) }
