// internal/payment/adapter.go
package payment

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/shopspring/decimal"
)

// PaymentAdapter 统一支付适配器接口
type PaymentAdapter interface {
	// CreateOrder 创建支付订单，返回支付参数
	CreateOrder(ctx context.Context, req *CreateOrderRequest) (*CreateOrderResponse, error)

	// QueryOrder 查询订单状态
	QueryOrder(ctx context.Context, tradeNo string) (*QueryOrderResponse, error)

	// Refund 退款
	Refund(ctx context.Context, req *RefundRequest) (*RefundResponse, error)

	// ParseNotify 解析异步回调通知
	ParseNotify(ctx context.Context, r *http.Request) (*NotifyResult, error)

	// NotifySuccess 返回回调成功响应
	NotifySuccess() string
}

// CreateOrderRequest 统一下单请求
type CreateOrderRequest struct {
	TradeNo   string            `json:"trade_no"`   // 系统订单号
	Amount    decimal.Decimal   `json:"amount"`     // 金额（元）
	Subject   string            `json:"subject"`    // 商品名称
	ClientIP  string            `json:"client_ip"`  // 客户端IP
	NotifyURL string            `json:"notify_url"` // 异步通知地址
	ReturnURL string            `json:"return_url"` // 同步跳转地址
	PayMethod string            `json:"pay_method"` // 支付方式: scan/h5/jsapi/app/web
	Extra     map[string]string `json:"extra"`      // 扩展参数
}

// CreateOrderResponse 统一下单响应
type CreateOrderResponse struct {
	PayType   string `json:"pay_type"`   // redirect(跳转) / qrcode(二维码) / jsapi(JS调起)
	PayURL    string `json:"pay_url"`    // 支付链接或二维码内容
	PayParams string `json:"pay_params"` // JSAPI 支付参数（JSON）
}

// QueryOrderResponse 查询订单响应
type QueryOrderResponse struct {
	TradeNo    string          `json:"trade_no"`
	ApiTradeNo string          `json:"api_trade_no"`
	Amount     decimal.Decimal `json:"amount"`
	Status     string          `json:"status"` // pending/paid/refunded/closed
	PaidAt     string          `json:"paid_at"`
}

// RefundRequest 退款请求
type RefundRequest struct {
	TradeNo     string          `json:"trade_no"`
	RefundNo    string          `json:"refund_no"`
	TotalAmount decimal.Decimal `json:"total_amount"`
	Amount      decimal.Decimal `json:"amount"`
	RefundDesc  string          `json:"refund_desc"`
}

// RefundResponse 退款响应
type RefundResponse struct {
	RefundNo     string          `json:"refund_no"`
	ApiRefundNo  string          `json:"api_refund_no"`
	Status       string          `json:"status"` // success/processing/failed
	ErrorMessage string          `json:"error_message,omitempty"`
	Amount       decimal.Decimal `json:"amount,omitempty"`
}

// RefundQuerier recovers uncertain refunds without creating another request.
type RefundQuerier interface {
	QueryRefund(context.Context, *RefundRequest) (*RefundResponse, error)
}

// NotifyResult 统一回调结果
type NotifyResult struct {
	TradeNo    string          `json:"trade_no"`     // 系统订单号
	ApiTradeNo string          `json:"api_trade_no"` // 上游订单号
	Amount     decimal.Decimal `json:"amount"`       // 支付金额
	Buyer      string          `json:"buyer"`        // 买家标识
	Status     string          `json:"status"`       // success / fail
}

// ChannelConfig 通道配置解析
type ChannelConfig struct {
	Raw json.RawMessage
}

func (c *ChannelConfig) Unmarshal(v interface{}) error {
	return json.Unmarshal(c.Raw, v)
}
