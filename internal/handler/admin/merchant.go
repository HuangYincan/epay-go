// internal/handler/admin/merchant.go
package admin

import (
	"strconv"

	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

// ListMerchants 商户列表
func ListMerchants(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	var status *int8
	if s := c.Query("status"); s != "" {
		v, _ := strconv.ParseInt(s, 10, 8)
		v8 := int8(v)
		status = &v8
	}

	merchantService := service.NewMerchantService()
	merchants, total, err := merchantService.List(page, pageSize, status)
	if err != nil {
		response.ServerError(c, "获取商户列表失败")
		return
	}

	response.SuccessPage(c, merchants, total, page, pageSize)
}

// GetMerchant 获取商户详情
func GetMerchant(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ParamError(c, "无效的商户ID")
		return
	}

	merchantService := service.NewMerchantService()
	merchant, err := merchantService.GetByID(id)
	if err != nil {
		response.NotFound(c, "商户不存在")
		return
	}

	response.Success(c, merchant)
}

// UpdateMerchantRequest 更新商户请求
type UpdateMerchantRequest struct {
	Email string `json:"email"`
	Phone string `json:"phone"`
}

// UpdateMerchant 更新商户信息
func UpdateMerchant(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ParamError(c, "无效的商户ID")
		return
	}

	var req UpdateMerchantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "参数错误")
		return
	}

	merchantService := service.NewMerchantService()
	merchant, err := merchantService.GetByID(id)
	if err != nil {
		response.NotFound(c, "商户不存在")
		return
	}

	merchant.Email = req.Email
	merchant.Phone = req.Phone

	if err := merchantService.UpdateProfile(id, merchant.Email, merchant.Phone); err != nil {
		response.ServerError(c, "保存资料失败")
		return
	}

	response.Success(c, merchant)
}

// UpdateMerchantStatusRequest 更新状态请求
type UpdateMerchantStatusRequest struct {
	Status int8 `json:"status" binding:"oneof=0 1"`
}

// UpdateMerchantStatus 更新商户状态
func UpdateMerchantStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ParamError(c, "无效的商户ID")
		return
	}

	var req UpdateMerchantStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "参数错误")
		return
	}

	merchantService := service.NewMerchantService()
	if err := merchantService.UpdateStatus(id, req.Status); err != nil {
		response.ServerError(c, err.Error())
		return
	}

	response.Success(c, nil)
}
