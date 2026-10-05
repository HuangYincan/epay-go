// internal/handler/admin/settlement.go
package admin

import (
	"strconv"

	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

// ListSettlements 结算列表
func ListSettlements(c *gin.Context) {
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

	settlementService := service.NewSettlementService()
	settlements, total, err := settlementService.List(page, pageSize, merchantID, status)
	if err != nil {
		response.ServerError(c, "获取结算列表失败")
		return
	}

	response.SuccessPage(c, settlements, total, page, pageSize)
}

// ApproveSettlement 审核通过
func ApproveSettlement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ParamError(c, "无效的结算ID")
		return
	}

	settlementService := service.NewSettlementService()
	if err := settlementService.Approve(id); err != nil {
		response.ServerError(c, err.Error())
		return
	}

	response.Success(c, nil)
}

// RejectSettlementRequest 驳回请求
type RejectSettlementRequest struct {
	Remark string `json:"remark" binding:"required"`
}

// RejectSettlement 驳回结算
func RejectSettlement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ParamError(c, "无效的结算ID")
		return
	}

	var req RejectSettlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "请填写驳回原因")
		return
	}

	settlementService := service.NewSettlementService()
	if err := settlementService.Reject(id, req.Remark); err != nil {
		response.ServerError(c, err.Error())
		return
	}

	response.Success(c, nil)
}

// CompleteSettlement records an administrator's confirmation of an actual payout.
func CompleteSettlement(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ParamError(c, "结算ID无效")
		return
	}
	if err := service.NewSettlementService().Complete(id); err != nil {
		response.Error(c, response.CodeParamError, err.Error())
		return
	}
	response.Success(c, nil)
}
