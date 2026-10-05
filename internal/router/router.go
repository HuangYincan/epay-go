// internal/router/router.go
package router

import (
	"github.com/example/epay-go/internal/handler/admin"
	"github.com/example/epay-go/internal/handler/merchant"
	"github.com/example/epay-go/internal/handler/payment"
	"github.com/example/epay-go/internal/middleware"
	"github.com/example/epay-go/pkg/jwt"
	"github.com/gin-gonic/gin"
)

// Setup 设置所有路由
func Setup(r *gin.Engine) {
	// 兼容旧版易支付接口
	r.GET("/submit.php", payment.LegacySubmit)
	r.POST("/submit.php", payment.LegacySubmit)
	r.POST("/mapi.php", payment.LegacyCreateOrder)
	r.GET("/api.php", payment.LegacyAPI)

	// 对外支付 API（无需登录）
	payAPI := r.Group("/api/pay")
	{
		payAPI.POST("/create", payment.CreateOrder)
		payAPI.GET("/query", payment.QueryOrder)
		payAPI.GET("/status/:trade_no", payment.PublicOrderStatus)
		payAPI.POST("/notify/:channel", payment.HandleNotify)
		payAPI.POST("/notify/:channel/:channel_id", payment.HandleNotify)
		payAPI.GET("/return/:channel", payment.HandleReturn)
		// 测试单专用：商户通知兜底（返回 success）
		payAPI.GET("/test-notify", payment.TestNotify)
	}

	r.GET("/api/cashier/:trade_no", payment.CashierOrder)
	r.POST("/api/cashier/:trade_no/pay", payment.CashierPay)
	// 管理后台 API
	adminAPI := r.Group("/api/admin")
	{
		// 无需认证
		adminAPI.POST("/auth/login", admin.Login)

		// 需要认证
		adminAuth := adminAPI.Group("")
		adminAuth.Use(middleware.JWTAuth(jwt.TokenTypeAdmin))
		{
			adminAuth.POST("/auth/logout", admin.Logout)
			adminAuth.PUT("/profile/password", admin.UpdatePassword)
			adminAuth.GET("/dashboard", admin.Dashboard)

			// 商户管理
			adminAuth.GET("/merchants", admin.ListMerchants)
			adminAuth.GET("/merchants/:id", admin.GetMerchant)
			adminAuth.PUT("/merchants/:id", admin.UpdateMerchant)
			adminAuth.PATCH("/merchants/:id/status", admin.UpdateMerchantStatus)

			// 订单管理
			adminAuth.GET("/orders", admin.ListOrders)
			adminAuth.GET("/orders/:trade_no", admin.GetOrder)
			adminAuth.POST("/orders/:trade_no/renotify", admin.RenotifyOrder)

			// 通道管理
			adminAuth.GET("/channels", admin.ListChannels)
			adminAuth.POST("/channels", admin.CreateChannel)
			adminAuth.GET("/channels/:id", admin.GetChannel)
			adminAuth.PUT("/channels/:id", admin.UpdateChannel)
			adminAuth.DELETE("/channels/:id", admin.DeleteChannel)

			// 插件配置
			adminAuth.GET("/plugins", admin.GetAllPluginConfigs)
			adminAuth.GET("/plugins/:plugin/config", admin.GetPluginConfig)

			// 测试支付
			adminAuth.POST("/test-payment", admin.TestPayment)

			// 结算管理
			adminAuth.GET("/settlements", admin.ListSettlements)
			adminAuth.PATCH("/settlements/:id/approve", admin.ApproveSettlement)
			adminAuth.PATCH("/settlements/:id/complete", admin.CompleteSettlement)
			adminAuth.PATCH("/settlements/:id/reject", admin.RejectSettlement)

			// 退款管理
			adminAuth.POST("/refunds", admin.CreateRefund)
			adminAuth.GET("/refunds", admin.ListRefunds)
			adminAuth.POST("/refunds/:refund_no/process", admin.ProcessRefund)
		}
	}

	// 商户端 API
	merchantAPI := r.Group("/api/merchant")
	{
		// 无需认证
		merchantAPI.POST("/auth/login", merchant.Login)
		merchantAPI.POST("/auth/register", merchant.Register)

		// 需要认证
		merchantAuth := merchantAPI.Group("")
		merchantAuth.Use(middleware.JWTAuth(jwt.TokenTypeMerchant))
		{
			merchantAuth.POST("/auth/logout", merchant.Logout)
			merchantAuth.GET("/dashboard", merchant.Dashboard)

			// 个人信息
			merchantAuth.GET("/profile", merchant.GetProfile)
			merchantAuth.PUT("/profile", merchant.UpdateProfile)
			merchantAuth.PUT("/profile/password", merchant.UpdatePassword)
			merchantAuth.POST("/profile/reset-key", merchant.ResetAPIKey)

			// 订单
			merchantAuth.GET("/orders", merchant.ListOrders)
			merchantAuth.GET("/orders/:trade_no", merchant.GetOrder)

			// 结算
			merchantAuth.GET("/settlements", merchant.ListSettlements)
			merchantAuth.POST("/settlements", merchant.ApplySettlement)

			// 资金记录
			merchantAuth.GET("/records", merchant.ListRecords)

			// 退款管理
			merchantAuth.POST("/refunds", merchant.CreateRefund)
			merchantAuth.GET("/refunds", merchant.ListRefunds)

			// 测试支付（商户侧）
			merchantAuth.POST("/test-payment", merchant.TestPayment)
		}
	}
}
