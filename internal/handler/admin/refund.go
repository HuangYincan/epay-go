// internal/handler/admin/refund.go
package admin

import (
	"strconv"

	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

// ListRefunds 退款列表
func ListRefunds(c *gin.Context) {
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

	refundService := service.NewRefundService()
	refunds, total, err := refundService.List(page, pageSize, merchantID, status)
	if err != nil {
		response.ServerError(c, "获取退款列表失败")
		return
	}

	response.SuccessPage(c, refunds, total, page, pageSize)
}

// ProcessRefundRequest 处理退款请求
type ProcessRefundRequest struct {
	Success    *bool  `json:"success" binding:"required"`
	FailReason string `json:"fail_reason"`
}

// CreateRefund 管理员创建退款申请
func CreateRefund(c *gin.Context) {
	var req service.CreateRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "参数错误: "+err.Error())
		return
	}

	refundService := service.NewRefundService()
	refund, err := refundService.CreateRefundByAdmin(&req)
	if err != nil {
		response.Error(c, response.CodeServerError, err.Error())
		return
	}

	response.Success(c, refund)
}

// ProcessRefund 处理退款
func ProcessRefund(c *gin.Context) {
	refundNo := c.Param("refund_no")

	var req ProcessRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "参数错误")
		return
	}

	refundService := service.NewRefundService()
	if err := refundService.ProcessRefund(refundNo, *req.Success, req.FailReason); err != nil {
		response.Error(c, response.CodeServerError, err.Error())
		return
	}

	response.Success(c, nil)
}
