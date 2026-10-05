// internal/model/order.go
package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// Order 订单
type Order struct {
	BaseModel
	TradeNo          string          `gorm:"size:32;uniqueIndex;not null" json:"trade_no"`
	OutTradeNo       string          `gorm:"size:64;not null;uniqueIndex:idx_merchant_order" json:"out_trade_no"`
	MerchantID       int64           `gorm:"index;not null;uniqueIndex:idx_merchant_order" json:"merchant_id"`
	ChannelID        int64           `gorm:"index" json:"channel_id"`
	PayType          string          `gorm:"size:20" json:"pay_type"`
	Amount           decimal.Decimal `gorm:"type:decimal(12,2);not null" json:"amount"`
	RealAmount       decimal.Decimal `gorm:"type:decimal(12,2)" json:"real_amount"`
	Fee              decimal.Decimal `gorm:"type:decimal(12,2);default:0" json:"fee"`
	Name             string          `gorm:"size:255" json:"name"`
	NotifyURL        string          `gorm:"size:512" json:"notify_url"`
	ReturnURL        string          `gorm:"size:512" json:"return_url"`
	ApiTradeNo       string          `gorm:"size:64" json:"api_trade_no"`
	Buyer            string          `gorm:"size:64" json:"buyer"`
	ClientIP         string          `gorm:"size:45" json:"client_ip"`
	Status           int8            `gorm:"default:0;index" json:"status"`  // 0未支付 1已支付 2已退款
	NotifyStatus     int8            `gorm:"default:0" json:"notify_status"` // 0未通知 1通知中 2已通知
	NotifyCount      int             `gorm:"default:0" json:"notify_count"`
	NextNotifyAt     *time.Time      `json:"next_notify_at"`
	NotifyLeaseUntil *time.Time      `json:"-"`
	NotifyLeaseToken string          `gorm:"size:32" json:"-"`
	QueryCount       int             `gorm:"default:0" json:"query_count"`
	NextQueryAt      *time.Time      `json:"next_query_at"`
	PaidAt           *time.Time      `json:"paid_at"`

	// 关联
	Merchant          *Merchant  `gorm:"foreignKey:MerchantID" json:"-"`
	Channel           *Channel   `gorm:"foreignKey:ChannelID" json:"-"`
	PayMethod         string     `gorm:"size:20" json:"pay_method"`
	PayURL            string     `gorm:"type:text" json:"-"`
	PayParams         string     `gorm:"type:text" json:"-"`
	ProviderNotifyURL string     `gorm:"size:512" json:"-"`
	CheckoutType      string     `gorm:"size:20" json:"-"`
	CheckoutExpiresAt *time.Time `json:"-"`
	Extra             string     `gorm:"type:text" json:"-"`
}

func (Order) TableName() string {
	return "orders"
}

// 订单状态常量
const (
	OrderStatusUnpaid = 0
	OrderStatusPaid   = 1
	OrderStatusRefund = 2
)

// 通知状态常量
const (
	NotifyStatusPending = 0
	NotifyStatusSending = 1
	NotifyStatusSuccess = 2
	NotifyStatusFailed  = 3
)
