// internal/handler/payment/query.go
package payment

import (
	"net/url"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/example/epay-go/pkg/sign"
	"github.com/gin-gonic/gin"
)

// QueryOrderRequest 查询订单请求
type QueryOrderRequest struct {
	Pid        string `form:"pid" binding:"required"`
	TradeNo    string `form:"trade_no"`
	OutTradeNo string `form:"out_trade_no"`
	Sign       string `form:"sign" binding:"required"`
}

// QueryOrder 查询订单
func QueryOrder(c *gin.Context) {
	var req QueryOrderRequest
	if err := c.ShouldBind(&req); err != nil {
		response.ParamError(c, "参数错误")
		return
	}

	if req.TradeNo == "" && req.OutTradeNo == "" {
		response.ParamError(c, "trade_no 和 out_trade_no 不能同时为空")
		return
	}

	merchantService := service.NewMerchantService()
	orderService := service.NewOrderService()

	// 获取商户
	resolved, err := resolveLegacyMerchant(merchantService, req.Pid)
	if err != nil {
		response.Error(c, response.CodeParamError, "商户不存在")
		return
	}
	merchant := resolved.Merchant
	if merchant.Status != 1 {
		response.Forbidden(c, "商户已被禁用")
		return
	}

	// 验证签名
	params := url.Values{}
	params.Set("pid", req.Pid)
	if req.TradeNo != "" {
		params.Set("trade_no", req.TradeNo)
	}
	if req.OutTradeNo != "" {
		params.Set("out_trade_no", req.OutTradeNo)
	}

	if !sign.VerifyMD5Sign(params, merchant.ApiKey, req.Sign) {
		response.Error(c, response.CodeParamError, "签名验证失败")
		return
	}

	// 查询订单
	var order *model.Order
	if req.TradeNo != "" {
		order, err = orderService.GetByTradeNo(req.TradeNo)
	} else {
		order, err = orderService.GetByOutTradeNo(merchant.ID, req.OutTradeNo)
	}

	if err != nil {
		response.NotFound(c, "订单不存在")
		return
	}
	if order.MerchantID != merchant.ID {
		response.NotFound(c, "订单不存在")
		return
	}

	response.Success(c, order)
}

// PublicOrderStatus 提供给 submit.php 页面轮询支付状态
func PublicOrderStatus(c *gin.Context) {
	tradeNo := c.Param("trade_no")
	if tradeNo == "" {
		response.ParamError(c, "订单号不能为空")
		return
	}

	orderService := service.NewOrderService()
	order, err := orderService.GetByTradeNo(tradeNo)
	if err != nil {
		response.NotFound(c, "订单不存在")
		return
	}

	paid := order.Status == model.OrderStatusPaid
	response.Success(c, gin.H{
		"trade_no":     order.TradeNo,
		"out_trade_no": order.OutTradeNo,
		"status":       order.Status,
		"paid":         paid,
		"return_url":   order.ReturnURL,
	})
}
