// internal/handler/admin/test_payment.go
package admin

import (
	"log"
	"strings"

	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

// TestPaymentRequest 测试支付请求
type TestPaymentRequest struct {
	OpenID    string `json:"openid"`
	ChannelID int64  `json:"channel_id" binding:"required"`
	Amount    string `json:"amount" binding:"required"`
	PayType   string `json:"pay_type" binding:"required"`
}

// TestPayment 测试支付
func TestPayment(c *gin.Context) {
	log.Printf("[DEBUG admin/test-payment] request received")
	var req TestPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[DEBUG admin/test-payment] bind error: %v", err)
		response.ParamError(c, "DEBUG_ADMIN_TEST_PAYMENT_BIND: "+err.Error())
		return
	}

	baseURL := getBaseURL(c)

	// 创建测试订单
	orderService := service.NewOrderService()
	order, payData, err := orderService.CreateTestOrder(req.ChannelID, req.Amount, req.PayType, baseURL, map[string]string{"openid": req.OpenID})
	if err != nil {
		response.Error(c, response.CodeServerError, err.Error())
		return
	}

	response.Success(c, gin.H{
		"order":    order,
		"pay_data": payData,
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
