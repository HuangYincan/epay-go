// internal/payment/wechat.go
package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/wechat/v3"
	"github.com/shopspring/decimal"
)

// WechatConfig 微信支付配置
type WechatConfig struct {
	PlatformPublicKey   string `json:"platform_public_key"`
	MchID               string `json:"mch_id"`                // 商户号
	AppID               string `json:"app_id"`                // 应用ID
	APIv3Key            string `json:"api_v3_key"`            // APIv3密钥
	SerialNo            string `json:"serial_no"`             // 证书序列号
	PrivateKey          string `json:"private_key"`           // 私钥内容
	PlatformSerialNo    string `json:"platform_serial_no"`    // 平台证书序列号
	PlatformCertContent string `json:"platform_cert_content"` // 平台证书内容
}

// WechatAdapter 微信支付适配器
type WechatAdapter struct {
	client *wechat.ClientV3
	config *WechatConfig
}

// NewWechatAdapter 创建微信支付适配器
func NewWechatAdapter(configJSON json.RawMessage) (PaymentAdapter, error) {
	// 兼容旧字段名（历史通道配置）
	var m map[string]interface{}
	_ = json.Unmarshal(configJSON, &m)
	if m != nil {
		// appid -> app_id
		if _, ok := m["app_id"]; !ok {
			if v, ok2 := m["appid"]; ok2 {
				m["app_id"] = v
			}
		}
		// apiv3_key / apiv3Key -> api_v3_key
		if _, ok := m["api_v3_key"]; !ok {
			if v, ok2 := m["apiv3_key"]; ok2 {
				m["api_v3_key"] = v
			} else if v, ok2 := m["apiv3Key"]; ok2 {
				m["api_v3_key"] = v
			}
		}
		// cert_serial_no -> serial_no
		if _, ok := m["serial_no"]; !ok {
			if v, ok2 := m["cert_serial_no"]; ok2 {
				m["serial_no"] = v
			}
		}
		b, _ := json.Marshal(m)
		configJSON = b
	}

	var cfg WechatConfig
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}

	client, err := wechat.NewClientV3(cfg.MchID, cfg.SerialNo, cfg.APIv3Key, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}

	// 设置平台证书
	switch {
	case cfg.PlatformPublicKey != "":
		err = client.AutoVerifySignByPublicKey([]byte(cfg.PlatformPublicKey), cfg.PlatformSerialNo)
	case cfg.PlatformCertContent != "":
		err = client.AutoVerifySignByCert([]byte(cfg.PlatformCertContent), cfg.PlatformSerialNo)
	default:
		err = client.AutoVerifySign(false)
	}
	if err != nil {
		return nil, err
	}

	return &WechatAdapter{
		client: client,
		config: &cfg,
	}, nil
}

// CreateOrder 创建支付订单
func (w *WechatAdapter) CreateOrder(ctx context.Context, req *CreateOrderRequest) (*CreateOrderResponse, error) {
	// 金额转为分
	amountFen := req.Amount.Mul(decimal.NewFromInt(100)).IntPart()

	bm := make(gopay.BodyMap)
	bm.Set("appid", w.config.AppID)
	bm.Set("mchid", w.config.MchID)
	bm.Set("description", req.Subject)
	bm.Set("out_trade_no", req.TradeNo)
	bm.Set("notify_url", req.NotifyURL)
	bm.SetBodyMap("amount", func(b gopay.BodyMap) {
		b.Set("total", amountFen)
		b.Set("currency", "CNY")
	})

	switch req.PayMethod {
	case "scan", "qrcode", "native", "precreate":
		// Native 扫码支付
		resp, err := w.client.V3TransactionNative(ctx, bm)
		if err != nil {
			return nil, err
		}
		if resp.Code != wechat.Success {
			return nil, errors.New(resp.Error)
		}
		return &CreateOrderResponse{
			PayType: "qrcode",
			PayURL:  resp.Response.CodeUrl,
		}, nil

	case "h5", "wap":
		// H5 支付
		bm.SetBodyMap("scene_info", func(b gopay.BodyMap) {
			b.Set("payer_client_ip", req.ClientIP)
			b.SetBodyMap("h5_info", func(h gopay.BodyMap) {
				h.Set("type", "Wap")
			})
		})
		resp, err := w.client.V3TransactionH5(ctx, bm)
		if err != nil {
			return nil, err
		}
		if resp.Code != wechat.Success {
			return nil, errors.New(resp.Error)
		}
		// 拼接 redirect_url
		payURL := resp.Response.H5Url
		if req.ReturnURL != "" {
			payURL += "&redirect_url=" + req.ReturnURL
		}
		return &CreateOrderResponse{
			PayType: "redirect",
			PayURL:  payURL,
		}, nil

	case "jsapi":
		// JSAPI 支付（需要 openid）
		openid := req.Extra["openid"]
		if openid == "" {
			return nil, errors.New("jsapi pay requires openid")
		}
		bm.SetBodyMap("payer", func(b gopay.BodyMap) {
			b.Set("openid", openid)
		})
		resp, err := w.client.V3TransactionJsapi(ctx, bm)
		if err != nil {
			return nil, err
		}
		if resp.Code != wechat.Success {
			return nil, errors.New(resp.Error)
		}
		// 生成 JSAPI 调起参数
		jsapiParams, err := w.client.PaySignOfJSAPI(w.config.AppID, resp.Response.PrepayId)
		if err != nil {
			return nil, err
		}
		paramsJSON, _ := json.Marshal(jsapiParams)
		return &CreateOrderResponse{
			PayType:   "jsapi",
			PayParams: string(paramsJSON),
		}, nil

	default:
		return nil, errors.New("unsupported pay method: " + req.PayMethod)
	}
}

// QueryOrder 查询订单
func (w *WechatAdapter) QueryOrder(ctx context.Context, tradeNo string) (*QueryOrderResponse, error) {
	resp, err := w.client.V3TransactionQueryOrder(ctx, wechat.OutTradeNo, tradeNo)
	if err != nil {
		return nil, err
	}
	if resp.Code != wechat.Success {
		return nil, errors.New(resp.Error)
	}

	status := "pending"
	switch resp.Response.TradeState {
	case "SUCCESS":
		status = "paid"
	case "CLOSED", "REVOKED", "PAYERROR":
		status = "closed"
	case "REFUND":
		status = "refunded"
	}

	// 金额从分转为元
	amount := decimal.NewFromInt(int64(resp.Response.Amount.Total)).Div(decimal.NewFromInt(100))

	return &QueryOrderResponse{
		TradeNo:    tradeNo,
		ApiTradeNo: resp.Response.TransactionId,
		Amount:     amount,
		Status:     status,
		PaidAt:     resp.Response.SuccessTime,
	}, nil
}

// Refund 退款
func (w *WechatAdapter) Refund(ctx context.Context, req *RefundRequest) (*RefundResponse, error) {
	// 金额转为分
	totalAmountFen := req.TotalAmount.Mul(decimal.NewFromInt(100)).IntPart()
	amountFen := req.Amount.Mul(decimal.NewFromInt(100)).IntPart()

	bm := make(gopay.BodyMap)
	bm.Set("out_trade_no", req.TradeNo)
	bm.Set("out_refund_no", req.RefundNo)
	bm.Set("reason", req.RefundDesc)
	bm.SetBodyMap("amount", func(b gopay.BodyMap) {
		b.Set("refund", amountFen)
		b.Set("total", totalAmountFen)
		b.Set("currency", "CNY")
	})

	resp, err := w.client.V3Refund(ctx, bm)
	if err != nil {
		return nil, err
	}

	if resp.Code >= 500 || resp.Code == http.StatusTooManyRequests {
		return nil, errors.New(resp.Error)
	}
	if resp.Code != wechat.Success {
		return &RefundResponse{
			RefundNo:     req.RefundNo,
			Status:       "failed",
			ErrorMessage: resp.Error,
		}, nil
	}

	status := "processing"
	if resp.Response.Status == "SUCCESS" {
		status = "success"
	}

	return &RefundResponse{
		RefundNo:    req.RefundNo,
		ApiRefundNo: resp.Response.RefundId,
		Status:      status,
	}, nil
}

// ParseNotify 解析回调通知
func (w *WechatAdapter) ParseNotify(ctx context.Context, r *http.Request) (*NotifyResult, error) {
	ts, err := strconv.ParseInt(r.Header.Get("Wechatpay-Timestamp"), 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > 5*time.Minute || time.Until(time.Unix(ts, 0)) > 5*time.Minute {
		return nil, errors.New("微信通知时间戳无效")
	}
	notifyReq, err := wechat.V3ParseNotify(r)
	if err != nil {
		return nil, err
	}

	if w.client == nil || w.client.WxPublicKeyMap()[r.Header.Get("Wechatpay-Serial")] == nil {
		return nil, errors.New("微信通知平台证书未知")
	}
	if err := notifyReq.VerifySignByPKMap(w.client.WxPublicKeyMap()); err != nil {
		return nil, err
	}
	// 解密支付回调内容
	result, err := notifyReq.DecryptPayCipherText(w.config.APIv3Key)
	if err != nil {
		return nil, err
	}
	if result.Appid != w.config.AppID || result.Mchid != w.config.MchID || result.Amount == nil || result.Payer == nil {
		return nil, errors.New("微信通知商户、应用或金额信息不匹配")
	}

	status := "fail"
	if result.TradeState == "SUCCESS" {
		status = "success"
	}

	// 金额从分转为元
	amount := decimal.NewFromInt(int64(result.Amount.Total)).Div(decimal.NewFromInt(100))

	return &NotifyResult{
		TradeNo:    result.OutTradeNo,
		ApiTradeNo: result.TransactionId,
		Amount:     amount,
		Buyer:      result.Payer.Openid,
		Status:     status,
	}, nil
}

// NotifySuccess 返回成功响应
func (w *WechatAdapter) NotifySuccess() string {
	return `{"code":"SUCCESS","message":"成功"}`
}

func init() {
	Register("wechat", NewWechatAdapter)
}

func (w *WechatAdapter) QueryRefund(ctx context.Context, req *RefundRequest) (*RefundResponse, error) {
	resp, err := w.client.V3RefundQuery(ctx, req.RefundNo, nil)
	if err != nil {
		return nil, err
	}
	if resp.Code != wechat.Success {
		if resp.Code == http.StatusNotFound {
			return &RefundResponse{Status: "not_found"}, nil
		}
		return nil, errors.New(resp.Error)
	}
	status := "processing"
	switch resp.Response.Status {
	case "SUCCESS":
		status = "success"
	case "CLOSED":
		status = "failed"
	}
	return &RefundResponse{RefundNo: req.RefundNo, ApiRefundNo: resp.Response.RefundId, Status: status, Amount: decimal.NewFromInt(int64(resp.Response.Amount.Refund)).Div(decimal.NewFromInt(100))}, nil
}
