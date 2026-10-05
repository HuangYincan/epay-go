// internal/handler/merchant/profile.go
package merchant

import (
	"github.com/example/epay-go/internal/middleware"
	"github.com/example/epay-go/internal/service"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

// GetProfile 获取个人信息
func GetProfile(c *gin.Context) {
	merchantID := middleware.GetUserID(c)

	merchantService := service.NewMerchantService()
	merchant, err := merchantService.GetByID(merchantID)
	if err != nil {
		response.NotFound(c, "商户不存在")
		return
	}

	response.Success(c, gin.H{
		"id":             merchant.ID,
		"username":       merchant.Username,
		"email":          merchant.Email,
		"phone":          merchant.Phone,
		"api_key":        merchant.ApiKey,
		"balance":        merchant.Balance,
		"frozen_balance": merchant.FrozenBalance,
		"status":         merchant.Status,
		"created_at":     merchant.CreatedAt,
	})
}

// UpdateProfileRequest 更新信息请求
type UpdateProfileRequest struct {
	Email string `json:"email" binding:"omitempty,email"`
	Phone string `json:"phone" binding:"omitempty"`
}

// UpdateProfile 更新个人信息
func UpdateProfile(c *gin.Context) {
	merchantID := middleware.GetUserID(c)

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "参数错误: "+err.Error())
		return
	}

	merchantService := service.NewMerchantService()
	merchant, err := merchantService.GetByID(merchantID)
	if err != nil {
		response.NotFound(c, "商户不存在")
		return
	}

	merchant.Email = req.Email
	merchant.Phone = req.Phone

	if err := merchantService.UpdateProfile(merchantID, merchant.Email, merchant.Phone); err != nil {
		response.ServerError(c, "保存资料失败")
		return
	}

	response.Success(c, nil)
}

// UpdatePasswordRequest 修改密码请求
type UpdatePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6,max=32"`
}

// UpdatePassword 修改密码
func UpdatePassword(c *gin.Context) {
	merchantID := middleware.GetUserID(c)

	var req UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ParamError(c, "参数错误: "+err.Error())
		return
	}

	merchantService := service.NewMerchantService()
	if err := merchantService.UpdatePassword(merchantID, req.OldPassword, req.NewPassword); err != nil {
		response.Error(c, response.CodeParamError, err.Error())
		return
	}

	response.Success(c, nil)
}

// ResetAPIKey 重置API密钥
func ResetAPIKey(c *gin.Context) {
	merchantID := middleware.GetUserID(c)

	merchantService := service.NewMerchantService()
	newKey, err := merchantService.ResetAPIKey(merchantID)
	if err != nil {
		response.ServerError(c, "重置密钥失败")
		return
	}

	response.Success(c, gin.H{
		"api_key": newKey,
	})
}
