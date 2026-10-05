package payment

import (
	"bytes"
	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	pay "github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/internal/service"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"strconv"
)

func HandleNotify(c *gin.Context) {
	plugin := c.Param("channel")
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 65537))
	if err != nil || len(body) > 65536 {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	var channels []model.Channel
	query := database.Get().Where("plugin = ?", plugin)
	if value := c.Param("channel_id"); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			c.String(http.StatusBadRequest, "fail")
			return
		}
		query = query.Where("id = ?", id)
	}
	// Disabled channels still need to settle payments initiated before they were disabled.
	if err := query.Order("id").Limit(100).Find(&channels).Error; err != nil {
		c.String(http.StatusOK, "fail")
		return
	}
	svc := service.NewOrderService()
	for _, channel := range channels {
		adapter, err := pay.NewAdapter(channel.Plugin, channel.Config)
		if err != nil {
			continue
		}
		req := c.Request.Clone(c.Request.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.Form = nil
		req.PostForm = nil
		result, err := adapter.ParseNotify(req.Context(), req)
		if err != nil || result == nil {
			continue
		}
		order, err := svc.GetByTradeNo(result.TradeNo)
		if err != nil || order.ChannelID != channel.ID {
			continue
		}
		if result.Status == "success" {
			if err := svc.ProcessPayNotify(result.TradeNo, result.ApiTradeNo, result.Buyer, result.Amount, channel.ID); err != nil {
				log.Printf("Process payment notification: %v", err)
				c.String(http.StatusOK, "fail")
				return
			}
			if paid, err := svc.GetByTradeNo(result.TradeNo); err == nil && paid.Status == model.OrderStatusPaid {
				go service.NewNotifyService().SendNotify(paid)
			}
		}
		if channel.Plugin == "wechat" {
			c.Data(http.StatusOK, "application/json", []byte(adapter.NotifySuccess()))
		} else {
			c.String(http.StatusOK, adapter.NotifySuccess())
		}
		return
	}
	c.String(http.StatusOK, "fail")
}

func HandleReturn(c *gin.Context) {
	order, err := service.NewOrderService().GetByTradeNo(c.Query("out_trade_no"))
	if err != nil {
		c.Redirect(http.StatusFound, "/")
		return
	}
	if order.ReturnURL != "" {
		c.Redirect(http.StatusFound, order.ReturnURL)
		return
	}
	c.Redirect(http.StatusFound, "/cashier/"+order.TradeNo)
}
