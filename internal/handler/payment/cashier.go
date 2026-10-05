package payment

import (
	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

func CashierOrder(c *gin.Context) {
	order, err := service.NewOrderService().GetByTradeNo(c.Param("trade_no"))
	if err != nil {
		response.NotFound(c, "订单不存在")
		return
	}
	response.Success(c, gin.H{"trade_no": order.TradeNo, "name": order.Name, "amount": order.Amount, "pay_type": order.PayType, "status": order.Status})
}
func CashierPay(c *gin.Context) {
	var req struct {
		PayType string `json:"pay_type" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "支付方式格式错误")
		return
	}
	data, err := service.NewOrderService().Checkout(c.Request.Context(), c.Param("trade_no"), req.PayType)
	if err != nil {
		response.ParamError(c, err.Error())
		return
	}
	response.Success(c, data)
}
