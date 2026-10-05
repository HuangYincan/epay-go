// internal/handler/admin/order.go
package admin

import (
	"strconv"

	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

// ListOrders 订单列表
func ListOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	var merchantID *int64
	if mid := c.Query("merchant_id"); mid != "" {
		v, _ := strconv.ParseInt(mid, 10, 64)
		merchantID = &v
	}

	var status *int8
	if s := c.Query("status"); s != "" {
		v, _ := strconv.ParseInt(s, 10, 8)
		v8 := int8(v)
		status = &v8
	}

	orderService := service.NewOrderService()
	orders, total, err := orderService.List(page, pageSize, merchantID, status)
	if err != nil {
		response.ServerError(c, "获取订单列表失败")
		return
	}

	response.SuccessPage(c, orders, total, page, pageSize)
}

// GetOrder 获取订单详情
func GetOrder(c *gin.Context) {
	tradeNo := c.Param("trade_no")

	orderService := service.NewOrderService()
	order, err := orderService.GetByTradeNo(tradeNo)
	if err != nil {
		response.NotFound(c, "订单不存在")
		return
	}

	response.Success(c, order)
}

// RenotifyOrder 重新发送通知
func RenotifyOrder(c *gin.Context) {
	tradeNo := c.Param("trade_no")

	orderService := service.NewOrderService()
	notifyService := service.NewNotifyService()

	order, err := orderService.GetByTradeNo(tradeNo)
	if err != nil {
		response.NotFound(c, "订单不存在")
		return
	}

	if order.Status != 1 {
		response.Error(c, response.CodeParamError, "订单未支付，无法发送通知")
		return
	}

	if err := notifyService.ResendNotify(order); err != nil {
		response.ServerError(c, "发送通知失败: "+err.Error())
		return
	}

	response.Success(c, nil)
}
