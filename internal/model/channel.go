// internal/model/channel.go
package model

import (
	"encoding/json"

	"github.com/shopspring/decimal"
)

// Channel 支付通道
type Channel struct {
	BaseModel
	Name        string          `gorm:"size:64;not null" json:"name"`
	Plugin      string          `gorm:"size:32;not null" json:"plugin"`          // alipay, wechat, paypal...
	PayTypes    string          `gorm:"size:255" json:"pay_types"`               // 支持的支付方式，逗号分隔
	AppType     string          `gorm:"size:100" json:"app_type"`                // 已启用的支付接口（逗号分隔，如"page,wap,qrcode"）
	Config      json.RawMessage `gorm:"type:jsonb" json:"config"`                // 通道配置
	CallbackURL string          `gorm:"size:512" json:"callback_url"`            // 完整回调地址，留空则自动使用当前请求域名拼接
	Rate        decimal.Decimal `gorm:"type:decimal(7,4);default:0" json:"rate"` // Percentage: 0.6 means 0.6%.
	DailyLimit  decimal.Decimal `gorm:"type:decimal(12,2);default:0" json:"daily_limit"`
	Status      int8            `gorm:"default:0" json:"status"` // 0禁用 1启用；创建必须显式启用
	Sort        int             `gorm:"default:0" json:"sort"`
}

func (Channel) TableName() string {
	return "channels"
}
