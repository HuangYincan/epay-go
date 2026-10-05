// pkg/jwt/jwt.go
package jwt

import (
	"errors"
	"time"

	"github.com/example/epay-go/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

type TokenType string

const (
	TokenTypeAdmin    TokenType = "admin"
	TokenTypeMerchant TokenType = "merchant"
)

type Claims struct {
	UserID    int64     `json:"user_id"`
	Username  string    `json:"username"`
	TokenType TokenType `json:"token_type"`
	jwt.RegisteredClaims
}

// GenerateToken 生成 JWT Token
func GenerateToken(userID int64, username string, tokenType TokenType) (string, error) {
	cfg := config.Get().JWT
	expireTime := time.Now().Add(time.Duration(cfg.ExpireHour) * time.Hour)

	claims := Claims{
		UserID:    userID,
		Username:  username,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expireTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "epay-go",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(cfg.Secret))
}

// ParseToken 解析 JWT Token
func ParseToken(tokenString string) (*Claims, error) {
	cfg := config.Get().JWT

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(cfg.Secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("epay-go"), jwt.WithExpirationRequired())

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}
