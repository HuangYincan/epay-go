package payment

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/sign"
	"github.com/example/epay-go/pkg/utils"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type LegacyCreateOrderRequest struct {
	OpenID     string `form:"openid" json:"openid"`
	Pid        string `form:"pid" json:"pid" binding:"required"`
	Type       string `form:"type" json:"type"`
	OutTradeNo string `form:"out_trade_no" json:"out_trade_no" binding:"required"`
	NotifyURL  string `form:"notify_url" json:"notify_url" binding:"required"`
	ReturnURL  string `form:"return_url" json:"return_url"`
	Name       string `form:"name" json:"name" binding:"required"`
	Money      string `form:"money" json:"money" binding:"required"`
	Sign       string `form:"sign" json:"sign" binding:"required"`
	SignType   string `form:"sign_type" json:"sign_type"`
	ClientType string `form:"clientip" json:"clientip"`
	Device     string `form:"device" json:"device"`
	PayMethod  string `form:"pay_method" json:"pay_method"`
}

type LegacyAPIRequest struct {
	Act        string `form:"act" json:"act" binding:"required"`
	Pid        string `form:"pid" json:"pid" binding:"required"`
	Key        string `form:"key" json:"key" binding:"required"`
	TradeNo    string `form:"trade_no" json:"trade_no"`
	OutTradeNo string `form:"out_trade_no" json:"out_trade_no"`
	Page       int    `form:"page" json:"page"`
	Limit      int    `form:"limit" json:"limit"`
	RefundNo   string `form:"refund_no" json:"refund_no"`
	Amount     string `form:"money" json:"money"`
	Reason     string `form:"reason" json:"reason"`
	NotifyURL  string `form:"notify_url" json:"notify_url"`
}

type legacyResolvedMerchant struct {
	Merchant *model.Merchant
	Pid      string
}

func LegacySubmit(c *gin.Context) {
	var req LegacyCreateOrderRequest
	if err := c.ShouldBind(&req); err != nil {
		legacyHTML(c, "参数错误: "+err.Error())
		return
	}

	orderResp, _, err := createLegacyOrder(c, &req)
	if err != nil {
		legacyHTML(c, err.Error())
		return
	}

	if orderResp.PayURL != "" {
		if orderResp.PayType == "qrcode" {
			legacyQRCodePage(c, &req, orderResp)
			return
		}
		c.Redirect(302, orderResp.PayURL)
		return
	}

	if orderResp.PayType == "jsapi" {
		c.Redirect(302, "/cashier/"+orderResp.TradeNo)
		return
	}
	legacyHTML(c, "未获取到支付跳转地址")
}

func LegacyCreateOrder(c *gin.Context) {
	var req LegacyCreateOrderRequest
	if err := c.ShouldBind(&req); err != nil {
		legacyError(c, "参数错误: "+err.Error())
		return
	}

	orderResp, resolved, err := createLegacyOrder(c, &req)
	if err != nil {
		legacyError(c, err.Error())
		return
	}

	resp := gin.H{
		"code":     1,
		"msg":      "success",
		"pid":      resolved.Pid,
		"trade_no": orderResp.TradeNo,
	}
	attachLegacyPayFields(resp, orderResp)
	c.JSON(200, resp)
}

func LegacyAPI(c *gin.Context) {
	var req LegacyAPIRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		legacyError(c, "参数错误: "+err.Error())
		return
	}

	merchantService := service.NewMerchantService()
	orderService := service.NewOrderService()
	refundService := service.NewRefundService()
	settlementService := service.NewSettlementService()

	resolved, err := resolveLegacyMerchant(merchantService, req.Pid)
	if err != nil {
		legacyError(c, "商户不存在")
		return
	}
	merchant := resolved.Merchant
	if merchant.Status != 1 {
		legacyError(c, "商户已被禁用")
		return
	}
	if merchant.ApiKey != req.Key {
		legacyError(c, "密钥错误")
		return
	}

	switch req.Act {
	case "query":
		c.JSON(200, gin.H{
			"code":    1,
			"msg":     "success",
			"pid":     merchant.ID,
			"user":    merchant.Username,
			"email":   merchant.Email,
			"status":  merchant.Status,
			"balance": merchant.Balance.StringFixed(2),
		})
	case "order":
		if req.TradeNo == "" && req.OutTradeNo == "" {
			legacyError(c, "trade_no 和 out_trade_no 不能同时为空")
			return
		}

		var order *model.Order
		if req.TradeNo != "" {
			order, err = orderService.GetByTradeNo(req.TradeNo)
		} else {
			order, err = orderService.GetByOutTradeNo(merchant.ID, req.OutTradeNo)
		}
		if err != nil || order.MerchantID != merchant.ID {
			legacyError(c, "订单不存在")
			return
		}

		status := 0
		if order.Status == model.OrderStatusPaid {
			status = 1
		}

		c.JSON(200, gin.H{
			"code":         1,
			"msg":          "success",
			"pid":          merchant.ID,
			"trade_no":     order.TradeNo,
			"out_trade_no": order.OutTradeNo,
			"type":         order.PayType,
			"name":         order.Name,
			"money":        order.Amount.StringFixed(2),
			"buyer":        order.Buyer,
			"api_trade_no": order.ApiTradeNo,
			"trade_status": status,
			"status":       status,
		})
	case "orders":
		page := req.Page
		if page <= 0 {
			page = 1
		}
		limit := req.Limit
		if limit <= 0 || limit > 100 {
			limit = 20
		}

		orders, total, err := orderService.List(page, limit, &merchant.ID, nil)
		if err != nil {
			legacyError(c, "获取订单列表失败")
			return
		}

		list := make([]gin.H, 0, len(orders))
		for _, order := range orders {
			status := 0
			if order.Status == model.OrderStatusPaid {
				status = 1
			}
			list = append(list, gin.H{
				"trade_no":     order.TradeNo,
				"out_trade_no": order.OutTradeNo,
				"type":         order.PayType,
				"name":         order.Name,
				"money":        order.Amount.StringFixed(2),
				"status":       status,
				"trade_status": status,
				"created_at":   order.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}

		c.JSON(200, gin.H{
			"code":  1,
			"msg":   "success",
			"pid":   merchant.ID,
			"total": total,
			"page":  page,
			"limit": limit,
			"data":  list,
		})
	case "settle":
		settlements, total, err := settlementService.List(1, 100, &merchant.ID, nil)
		if err != nil {
			legacyError(c, "获取结算记录失败")
			return
		}

		list := make([]gin.H, 0, len(settlements))
		for _, item := range settlements {
			list = append(list, gin.H{
				"settle_no":    item.SettleNo,
				"money":        item.Amount.StringFixed(2),
				"fee":          item.Fee.StringFixed(2),
				"actual_money": item.ActualAmount.StringFixed(2),
				"account_type": item.AccountType,
				"account_no":   item.AccountNo,
				"account_name": item.AccountName,
				"status":       item.Status,
				"remark":       item.Remark,
				"created_at":   item.CreatedAt.Format("2006-01-02 15:04:05"),
			})
		}

		c.JSON(200, gin.H{
			"code":  1,
			"msg":   "success",
			"pid":   merchant.ID,
			"total": total,
			"data":  list,
		})
	case "refund":
		if req.TradeNo == "" && req.OutTradeNo == "" {
			legacyError(c, "trade_no 和 out_trade_no 不能同时为空")
			return
		}
		if req.Amount == "" {
			legacyError(c, "退款金额不能为空")
			return
		}

		tradeNo := req.TradeNo
		if tradeNo == "" {
			order, err := orderService.GetByOutTradeNo(merchant.ID, req.OutTradeNo)
			if err != nil {
				legacyError(c, "订单不存在")
				return
			}
			tradeNo = order.TradeNo
		}

		refund, err := refundService.CreateRefund(merchant.ID, &service.CreateRefundRequest{
			TradeNo:   tradeNo,
			Amount:    req.Amount,
			Reason:    req.Reason,
			NotifyURL: req.NotifyURL,
		})
		if err != nil {
			legacyError(c, err.Error())
			return
		}

		c.JSON(200, gin.H{
			"code":         1,
			"msg":          "success",
			"pid":          merchant.ID,
			"refund_no":    refund.RefundNo,
			"trade_no":     refund.TradeNo,
			"out_trade_no": req.OutTradeNo,
			"money":        refund.Amount,
			"status":       refund.Status,
		})
	default:
		legacyError(c, "暂不支持的 act: "+req.Act)
	}
}

func createLegacyOrder(c *gin.Context, req *LegacyCreateOrderRequest) (*service.CreateOrderResponse, *legacyResolvedMerchant, error) {
	merchantService := service.NewMerchantService()
	orderService := service.NewOrderService()

	resolved, err := resolveLegacyMerchant(merchantService, req.Pid)
	if err != nil {
		return nil, nil, fmt.Errorf("商户不存在")
	}
	merchant := resolved.Merchant
	if merchant.Status != 1 {
		return nil, nil, fmt.Errorf("商户已被禁用")
	}

	params := url.Values{}
	params.Set("pid", req.Pid)
	if req.Type != "" {
		params.Set("type", req.Type)
	}
	params.Set("out_trade_no", req.OutTradeNo)
	params.Set("notify_url", req.NotifyURL)
	params.Set("name", req.Name)
	params.Set("money", req.Money)
	if req.ReturnURL != "" {
		params.Set("return_url", req.ReturnURL)
	}
	if req.Device != "" {
		params.Set("device", req.Device)
	}
	if req.PayMethod != "" {
		params.Set("pay_method", req.PayMethod)
	}
	if req.OpenID != "" {
		params.Set("openid", req.OpenID)
	}
	if req.ClientType != "" {
		params.Set("clientip", req.ClientType)
	}

	if !sign.VerifyMD5Sign(params, merchant.ApiKey, req.Sign) {
		return nil, nil, fmt.Errorf("签名验证失败")
	}

	amount, err := decimal.NewFromString(req.Money)
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		return nil, nil, fmt.Errorf("金额格式错误")
	}

	routing, err := resolvePayRouting(req.Type, req.PayMethod)
	if err != nil {
		return nil, nil, err
	}

	orderResp, err := orderService.Create(c.Request.Context(), &service.CreateOrderRequest{
		MerchantID:        merchant.ID,
		OutTradeNo:        req.OutTradeNo,
		Amount:            amount,
		Name:              req.Name,
		PayType:           routing.PayType,
		NotifyURL:         req.NotifyURL,
		MerchantNotifyURL: req.NotifyURL,
		PlatformBaseURL:   getPaymentBaseURL(c),
		ReturnURL:         req.ReturnURL,
		ClientIP:          utils.GetClientIP(c),
		PayMethod:         routing.PayMethod,
		Extra:             map[string]string{"openid": req.OpenID},
	})
	if err != nil {
		return nil, nil, err
	}

	return orderResp, resolved, nil
}

func resolveLegacyMerchant(merchantService *service.MerchantService, pid string) (*legacyResolvedMerchant, error) {
	if id, err := strconv.ParseInt(pid, 10, 64); err == nil && id > 0 {
		merchant, getErr := merchantService.GetByID(id)
		if getErr == nil {
			return &legacyResolvedMerchant{Merchant: merchant, Pid: pid}, nil
		}
	}

	merchant, err := merchantService.GetByAPIKey(pid)
	if err != nil {
		return nil, err
	}
	return &legacyResolvedMerchant{Merchant: merchant, Pid: strconv.FormatInt(merchant.ID, 10)}, nil
}
func attachLegacyPayFields(resp gin.H, orderResp *service.CreateOrderResponse) {
	if orderResp.PayParams != "" {
		resp["pay_type"] = orderResp.PayType
		resp["pay_params"] = orderResp.PayParams
		resp["payurl"] = "/cashier/" + orderResp.TradeNo
	}
	if orderResp.PayURL == "" {
		return
	}
	resp["payurl"] = orderResp.PayURL
	switch orderResp.PayType {
	case "qrcode":
		resp["qrcode"] = orderResp.PayURL
	case "redirect":
		resp["payurl"] = orderResp.PayURL
	case "jsapi":
		resp["urlscheme"] = orderResp.PayURL
	}
}

func legacyHTML(c *gin.Context, msg string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, "<html><body><h3>"+template.HTMLEscapeString(msg)+"</h3></body></html>")
}

func legacyQRCodePage(c *gin.Context, req *LegacyCreateOrderRequest, orderResp *service.CreateOrderResponse) {
	escapedName := template.HTMLEscapeString(req.Name)
	escapedAmount := template.HTMLEscapeString(req.Money)
	escapedTradeNo := template.HTMLEscapeString(orderResp.TradeNo)
	escapedOutTradeNo := template.HTMLEscapeString(req.OutTradeNo)
	escapedPayURLHTML := template.HTMLEscapeString(orderResp.PayURL)
	escapedPayURL := template.JSEscapeString(orderResp.PayURL)
	statusAPIURL := template.JSEscapeString(getPaymentBaseURL(c) + "/api/pay/status/" + orderResp.TradeNo)
	escapedReturnURL := template.HTMLEscapeString(req.ReturnURL)
	escapedReturnURLJS := template.JSEscapeString(req.ReturnURL)
	qrImageURL := template.HTMLEscapeString("https://api.qrserver.com/v1/create-qr-code/?size=232x232&data=" + url.QueryEscape(orderResp.PayURL))
	returnSection := ""
	if strings.TrimSpace(req.ReturnURL) != "" {
		returnSection = fmt.Sprintf(`<a class="action secondary" href="%s">返回商户页面</a>`, escapedReturnURL)
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>请扫码完成支付</title>
  <style>
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: radial-gradient(circle at top, #1f4b99 0%%, #0b1220 42%%, #060913 100%%);
      color: #e5eefc;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 24px;
    }
    .panel {
      width: 100%%;
      max-width: 980px;
      background: rgba(12, 18, 33, 0.92);
      border: 1px solid rgba(148, 163, 184, 0.18);
      border-radius: 28px;
      box-shadow: 0 24px 80px rgba(0, 0, 0, 0.35);
      overflow: hidden;
      display: grid;
      grid-template-columns: 1.1fr 0.9fr;
      backdrop-filter: blur(16px);
    }
    .hero {
      padding: 42px;
      background: linear-gradient(180deg, rgba(59, 130, 246, 0.18) 0%%, rgba(37, 99, 235, 0.05) 100%%);
      display: flex;
      flex-direction: column;
      justify-content: space-between;
    }
    .badge {
      display: inline-flex;
      align-items: center;
      padding: 8px 14px;
      border-radius: 999px;
      background: rgba(37, 99, 235, 0.18);
      color: #bfdbfe;
      font-size: 13px;
      letter-spacing: 0.06em;
    }
    .title {
      margin: 20px 0 10px;
      font-size: 34px;
      font-weight: 700;
      color: #f8fbff;
    }
    .subtitle {
      margin: 0;
      line-height: 1.75;
      color: #cbd5e1;
      font-size: 15px;
    }
    .summary {
      margin-top: 32px;
      display: grid;
      gap: 14px;
    }
    .summary-item {
      padding: 16px 18px;
      border-radius: 18px;
      background: rgba(15, 23, 42, 0.55);
      border: 1px solid rgba(148, 163, 184, 0.12);
    }
    .summary-label {
      color: #94a3b8;
      font-size: 12px;
      margin-bottom: 6px;
    }
    .summary-value {
      color: #f8fafc;
      font-size: 16px;
      word-break: break-all;
    }
    .summary-value.amount {
      font-size: 30px;
      font-weight: 700;
      color: #7dd3fc;
    }
    .pay-box {
      padding: 42px 32px;
      background: linear-gradient(180deg, rgba(255,255,255,0.98) 0%%, rgba(241,245,249,0.98) 100%%);
      color: #0f172a;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
    }
    .qr-shell {
      width: 260px;
      height: 260px;
      padding: 14px;
      background: #fff;
      border-radius: 24px;
      box-shadow: 0 18px 40px rgba(15, 23, 42, 0.16);
      display: flex;
      align-items: center;
      justify-content: center;
    }
    .qr-shell > div,
    .qr-shell canvas,
    .qr-shell img {
      max-width: 100%%;
      max-height: 100%%;
    }
    .pay-title {
      margin: 24px 0 8px;
      font-size: 24px;
      font-weight: 700;
      color: #0f172a;
    }
    .pay-desc {
      margin: 0 0 18px;
      color: #475569;
      text-align: center;
      line-height: 1.7;
    }
    .status-chip {
      margin: 0 0 16px;
      display: inline-flex;
      align-items: center;
      gap: 8px;
      padding: 10px 14px;
      border-radius: 999px;
      background: #e0f2fe;
      color: #075985;
      font-size: 13px;
      font-weight: 600;
    }
    .status-chip.success {
      background: #dcfce7;
      color: #166534;
    }
    .actions {
      display: flex;
      gap: 12px;
      flex-wrap: wrap;
      justify-content: center;
      margin-top: 8px;
    }
    .action {
      appearance: none;
      border: none;
      cursor: pointer;
      text-decoration: none;
      padding: 12px 18px;
      border-radius: 14px;
      font-size: 14px;
      font-weight: 600;
      transition: transform 0.18s ease, box-shadow 0.18s ease, opacity 0.18s ease;
    }
    .action:hover {
      transform: translateY(-1px);
    }
    .action.primary {
      background: linear-gradient(135deg, #2563eb 0%%, #1d4ed8 100%%);
      color: #fff;
      box-shadow: 0 12px 24px rgba(37, 99, 235, 0.28);
    }
    .action.secondary {
      background: #e2e8f0;
      color: #0f172a;
    }
    .link-box {
      width: 100%%;
      margin-top: 18px;
      padding: 12px 14px;
      border-radius: 14px;
      background: #f8fafc;
      border: 1px solid #cbd5e1;
      color: #475569;
      font-size: 12px;
      line-height: 1.6;
      word-break: break-all;
    }
    @media (max-width: 860px) {
      .panel {
        grid-template-columns: 1fr;
      }
      .hero, .pay-box {
        padding: 28px 22px;
      }
      .title {
        font-size: 28px;
      }
      .qr-shell {
        width: 220px;
        height: 220px;
      }
    }
  </style>
</head>
<body>
  <div class="panel">
    <section class="hero">
      <div>
        <div class="badge">安全支付 · 扫码付款</div>
        <h1 class="title">请使用微信扫码完成支付</h1>
        <p class="subtitle">订单已经创建成功，请使用微信扫一扫扫描右侧二维码完成付款。支付成功后，系统会自动通知商户并跳转回业务页面。</p>
        <div class="summary">
          <div class="summary-item">
            <div class="summary-label">支付金额</div>
            <div class="summary-value amount">¥%s</div>
          </div>
          <div class="summary-item">
            <div class="summary-label">商品名称</div>
            <div class="summary-value">%s</div>
          </div>
          <div class="summary-item">
            <div class="summary-label">平台订单号</div>
            <div class="summary-value">%s</div>
          </div>
          <div class="summary-item">
            <div class="summary-label">商户订单号</div>
            <div class="summary-value">%s</div>
          </div>
        </div>
      </div>
    </section>
    <section class="pay-box">
      <div class="qr-shell"><img src="%s" alt="支付二维码" /></div>
      <h2 class="pay-title">扫码支付</h2>
      <p class="pay-desc">如果二维码未显示，请点击下方按钮重新打开支付链接，或复制链接到支持的环境中打开。</p>
      <div class="status-chip" id="pay-status">等待支付完成</div>
      <div class="actions">
        <button class="action primary" type="button" onclick="window.location.href=payUrl">打开支付链接</button>
        %s
      </div>
      <div class="link-box">%s</div>
    </section>
  </div>
  <script>
    const payUrl = "%s";
    const statusUrl = "%s";
    const returnUrl = "%s";
    const statusEl = document.getElementById('pay-status');
    let redirected = false;

    async function checkPaymentStatus() {
      if (redirected) {
        return;
      }
      try {
        const response = await fetch(statusUrl, {
          method: 'GET',
          headers: { 'Accept': 'application/json' },
          cache: 'no-store'
        });
        const result = await response.json();
        if (result && result.code === 0 && result.data && result.data.paid) {
          redirected = true;
          statusEl.textContent = '支付成功，正在跳转...';
          statusEl.classList.add('success');
          window.setTimeout(function () {
            if (returnUrl) {
              window.location.href = returnUrl;
              return;
            }
            window.location.reload();
          }, 1200);
          return;
        }
        statusEl.textContent = '等待支付完成';
      } catch (error) {
        statusEl.textContent = '支付状态检查中...';
      }
    }

    checkPaymentStatus();
    window.setInterval(checkPaymentStatus, 3000);
  </script>
</body>
</html>`, escapedAmount, escapedName, escapedTradeNo, escapedOutTradeNo, qrImageURL, returnSection, escapedPayURLHTML, escapedPayURL, statusAPIURL, escapedReturnURLJS)

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

func legacyError(c *gin.Context, msg string) {
	c.JSON(200, gin.H{
		"code": -1,
		"msg":  msg,
	})
}
