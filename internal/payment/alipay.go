// internal/payment/alipay.go
package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
	"github.com/shopspring/decimal"
)

// AlipayConfig 支付宝配置
type AlipayConfig struct {
	AppID        string `json:"app_id"`
	PrivateKey   string `json:"private_key"`
	PublicKey    string `json:"public_key"`     // 支付宝公钥
	AppPublicKey string `json:"app_public_key"` // 应用公钥证书（可选）
	IsProd       bool   `json:"is_prod"`        // 是否生产环境
	SignType     string `json:"sign_type"`      // RSA2
}

// AlipayAdapter 支付宝适配器
type AlipayAdapter struct {
	client *alipay.Client
	config *AlipayConfig
}

// NewAlipayAdapter 创建支付宝适配器
func NewAlipayAdapter(configJSON json.RawMessage) (PaymentAdapter, error) {
	// 兼容旧字段名（历史通道配置）
	var m map[string]interface{}
	_ = json.Unmarshal(configJSON, &m)
	if m != nil {
		// Old admin forms stored boolean select values as strings.
		if value, ok := m["is_prod"].(string); ok {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, errors.New("支付宝生产环境选项必须为true或false")
			}
			m["is_prod"] = parsed
		}
		// appid -> app_id
		if _, ok := m["app_id"]; !ok {
			if v, ok2 := m["appid"]; ok2 {
				m["app_id"] = v
			}
		}
		// app_private_key -> private_key
		if _, ok := m["private_key"]; !ok {
			if v, ok2 := m["app_private_key"]; ok2 {
				m["private_key"] = v
			}
		}
		// alipay_public_key -> public_key
		if _, ok := m["public_key"]; !ok {
			if v, ok2 := m["alipay_public_key"]; ok2 {
				m["public_key"] = v
			}
		}
		// cert_mode -> is_prod/sign_type (无法可靠推断，保持默认即可)
		if _, ok := m["sign_type"]; !ok {
			m["sign_type"] = "RSA2"
		}
		b, _ := json.Marshal(m)
		configJSON = b
	}

	var cfg AlipayConfig
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}

	client, err := alipay.NewClient(cfg.AppID, cfg.PrivateKey, cfg.IsProd)
	if err != nil {
		return nil, err
	}

	client.SetHttpClient(newProviderHTTPClient())
	cfg.PublicKey, err = normalizeRSAPublicKey(cfg.PublicKey)
	if err != nil {
		return nil, err
	}
	client.AutoVerifySign([]byte(cfg.PublicKey))

	return &AlipayAdapter{
		client: client,
		config: &cfg,
	}, nil
}

// CreateOrder 创建支付订单
func (a *AlipayAdapter) CreateOrder(ctx context.Context, req *CreateOrderRequest) (*CreateOrderResponse, error) {
	amount := req.Amount.StringFixed(2)

	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", req.TradeNo)
	bm.Set("total_amount", amount)
	bm.Set("subject", req.Subject)
	bm.Set("notify_url", req.NotifyURL)

	switch req.PayMethod {
	case "scan", "qrcode":
		// 扫码支付
		bm.Set("product_code", "FACE_TO_FACE_PAYMENT")
		resp, err := a.client.TradePrecreate(ctx, bm)
		if err != nil {
			return nil, err
		}
		if resp.Response.Code != "10000" {
			return nil, errors.New(resp.Response.SubMsg)
		}
		return &CreateOrderResponse{
			PayType: "qrcode",
			PayURL:  resp.Response.QrCode,
		}, nil

	case "h5", "wap":
		// H5支付
		bm.Set("product_code", "QUICK_WAP_WAY")
		bm.Set("return_url", req.ReturnURL)
		payURL, err := a.client.TradeWapPay(ctx, bm)
		if err != nil {
			return nil, err
		}
		return &CreateOrderResponse{
			PayType: "redirect",
			PayURL:  payURL,
		}, nil

	case "web", "pc", "page":
		// PC网页支付
		bm.Set("product_code", "FAST_INSTANT_TRADE_PAY")
		bm.Set("return_url", req.ReturnURL)
		payURL, err := a.client.TradePagePay(ctx, bm)
		if err != nil {
			return nil, err
		}
		return &CreateOrderResponse{
			PayType: "redirect",
			PayURL:  payURL,
		}, nil

	default:
		return nil, errors.New("unsupported pay method: " + req.PayMethod)
	}
}

// QueryOrder 查询订单
func (a *AlipayAdapter) QueryOrder(ctx context.Context, tradeNo string) (*QueryOrderResponse, error) {
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", tradeNo)

	resp, err := a.client.TradeQuery(ctx, bm)
	if err != nil {
		return nil, err
	}

	if resp.Response.Code != "10000" {
		return nil, errors.New(resp.Response.SubMsg)
	}

	status := "pending"
	switch resp.Response.TradeStatus {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		status = "paid"
	case "TRADE_CLOSED":
		status = "closed"
	}

	amount, _ := decimal.NewFromString(resp.Response.TotalAmount)

	return &QueryOrderResponse{
		TradeNo:    tradeNo,
		ApiTradeNo: resp.Response.TradeNo,
		Amount:     amount,
		Status:     status,
		PaidAt:     resp.Response.SendPayDate,
	}, nil
}

// Refund 退款
func (a *AlipayAdapter) Refund(ctx context.Context, req *RefundRequest) (*RefundResponse, error) {
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", req.TradeNo)
	bm.Set("out_request_no", req.RefundNo)
	bm.Set("refund_amount", req.Amount.StringFixed(2))
	if req.RefundDesc != "" {
		bm.Set("refund_reason", req.RefundDesc)
	}

	resp, err := a.client.TradeRefund(ctx, bm)
	if err != nil {
		return nil, err
	}

	if resp.Response.Code == "20000" || resp.Response.SubCode == "ACQ.SYSTEM_ERROR" {
		return nil, errors.New(resp.Response.SubMsg)
	}
	if resp.Response.Code != "10000" {
		return &RefundResponse{
			RefundNo:     req.RefundNo,
			Status:       "failed",
			ErrorMessage: resp.Response.SubMsg,
		}, nil
	}

	return &RefundResponse{
		RefundNo:    req.RefundNo,
		ApiRefundNo: resp.Response.TradeNo,
		Status:      "success",
	}, nil
}

// ParseNotify 解析回调通知
func (a *AlipayAdapter) ParseNotify(ctx context.Context, r *http.Request) (*NotifyResult, error) {
	notifyReq, err := alipay.ParseNotifyToBodyMap(r)
	if err != nil {
		return nil, err
	}

	// 验签
	ok, err := alipay.VerifySign(a.config.PublicKey, notifyReq)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("签名验证失败")
	}
	if notifyReq.Get("app_id") != a.config.AppID {
		return nil, errors.New("支付宝通知应用不匹配")
	}

	tradeStatus := notifyReq.Get("trade_status")
	status := "fail"
	if tradeStatus == "TRADE_SUCCESS" || tradeStatus == "TRADE_FINISHED" {
		status = "success"
	}

	amount, _ := decimal.NewFromString(notifyReq.Get("total_amount"))

	return &NotifyResult{
		TradeNo:    notifyReq.Get("out_trade_no"),
		ApiTradeNo: notifyReq.Get("trade_no"),
		Amount:     amount,
		Buyer:      notifyReq.Get("buyer_id"),
		Status:     status,
	}, nil
}

// NotifySuccess 返回成功响应
func (a *AlipayAdapter) NotifySuccess() string {
	return "success"
}

func init() {
	Register("alipay", NewAlipayAdapter)
}

func (a *AlipayAdapter) QueryRefund(ctx context.Context, req *RefundRequest) (*RefundResponse, error) {
	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", req.TradeNo)
	bm.Set("out_request_no", req.RefundNo)
	resp, err := a.client.TradeFastPayRefundQuery(ctx, bm)
	if err != nil {
		return nil, err
	}
	if resp.Response.Code != "10000" {
		if resp.Response.SubCode == "ACQ.TRADE_NOT_EXIST" {
			return &RefundResponse{Status: "not_found"}, nil
		}
		return nil, errors.New(resp.Response.SubMsg)
	}
	// Official API: absent refund_status means request not received or not successful.
	// Reissue only with the same out_request_no and amount after the worker delay.
	status := "not_found"
	if resp.Response.RefundStatus == "REFUND_SUCCESS" {
		status = "success"
	}
	amount := decimal.Zero
	if status == "success" {
		var err error
		amount, err = decimal.NewFromString(resp.Response.RefundAmount)
		if err != nil {
			return nil, err
		}
	}
	return &RefundResponse{RefundNo: req.RefundNo, ApiRefundNo: resp.Response.TradeNo, Status: status, Amount: amount}, nil
}
