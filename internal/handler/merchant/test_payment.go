package merchant

import (
	"strings"

	"github.com/example/epay-go/internal/middleware"
	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/example/epay-go/pkg/utils"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type TestPaymentRequest struct {
	OpenID    string `json:"openid"`
	Amount    string `json:"amount" binding:"required"`
	PayType   string `json:"pay_type" binding:"required"` // alipay, wxpay
	PayMethod string `json:"pay_method" binding:"omitempty,oneof=scan native h5 jsapi web"`
}

// TestPayment 商户侧测试支付（创建一笔当前商户的测试订单）
func TestPayment(c *gin.Context) {
	merchantID := middleware.GetUserID(c)

	var req TestPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "参数错误")
		return
	}

	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		response.ParamError(c, "金额格式错误")
		return
	}

	baseURL := getBaseURL(c)

	outTradeNo := "MTEST" + utils.GenerateTradeNo()
	orderReq := &service.CreateOrderRequest{
		MerchantID:      merchantID,
		OutTradeNo:      outTradeNo,
		Amount:          amount,
		Name:            "商户测试支付",
		PayType:         req.PayType,
		PlatformBaseURL: baseURL,
		ClientIP:        utils.GetClientIP(c),
		PayMethod:       req.PayMethod,
		Extra:           map[string]string{"openid": req.OpenID},
	}

	orderService := service.NewOrderService()
	orderResp, err := orderService.Create(c.Request.Context(), orderReq)
	if err != nil {
		response.Error(c, response.CodeServerError, err.Error())
		return
	}

	order, err := orderService.GetByTradeNo(orderResp.TradeNo)
	if err != nil {
		response.Error(c, response.CodeServerError, "订单创建成功但查询失败")
		return
	}

	response.Success(c, gin.H{
		"order": order,
		"pay_data": gin.H{
			"pay_type":   orderResp.PayType,
			"pay_url":    orderResp.PayURL,
			"pay_params": orderResp.PayParams,
		},
	})
}

func getBaseURL(c *gin.Context) string {
	scheme := "http"
	if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if c.Request.TLS != nil {
		scheme = "https"
	}

	host := c.Request.Host
	if host == "" {
		host = "localhost"
	}
	host = strings.TrimSpace(host)
	return scheme + "://" + host
}
