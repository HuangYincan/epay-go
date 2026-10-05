// internal/middleware/auth.go
package middleware

import (
	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"strings"

	"github.com/example/epay-go/pkg/jwt"
	"github.com/example/epay-go/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	ContextKeyUserID    = "user_id"
	ContextKeyUsername  = "username"
	ContextKeyTokenType = "token_type"
)

// JWTAuth JWT 认证中间件
func JWTAuth(requiredType jwt.TokenType) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			response.Unauthorized(c, "请先登录")
			c.Abort()
			return
		}

		// Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.Unauthorized(c, "Token 格式错误")
			c.Abort()
			return
		}

		claims, err := jwt.ParseToken(parts[1])
		if err != nil {
			response.Unauthorized(c, "Token 无效或已过期")
			c.Abort()
			return
		}

		// 验证 token 类型
		if claims.TokenType != requiredType {
			response.Forbidden(c, "无权访问")
			c.Abort()
			return
		}

		// 将用户信息存入上下文
		if requiredType == jwt.TokenTypeMerchant {
			var merchant model.Merchant
			if err := database.Get().First(&merchant, claims.UserID).Error; err != nil || merchant.Status != 1 {
				response.Forbidden(c, "商户不存在或已禁用")
				c.Abort()
				return
			}
		}
		c.Set(ContextKeyUserID, claims.UserID)
		c.Set(ContextKeyUsername, claims.Username)
		c.Set(ContextKeyTokenType, claims.TokenType)

		c.Next()
	}
}

// GetUserID 从上下文获取用户ID
func GetUserID(c *gin.Context) int64 {
	if id, exists := c.Get(ContextKeyUserID); exists {
		return id.(int64)
	}
	return 0
}

// GetUsername 从上下文获取用户名
func GetUsername(c *gin.Context) string {
	if name, exists := c.Get(ContextKeyUsername); exists {
		return name.(string)
	}
	return ""
}
