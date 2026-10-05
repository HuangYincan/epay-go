// internal/model/refund.go
package model

import (
	"gorm.io/gorm"
	"time"
)

// Refund 退款订单
type Refund struct {
	FundsReserved  bool           `gorm:"default:false" json:"-"`
	NextQueryAt    *time.Time     `gorm:"index" json:"-"`
	ID             int64          `gorm:"primaryKey" json:"id"`
	RefundNo       string         `gorm:"size:32;uniqueIndex;not null" json:"refund_no"`     // 退款单号
	TradeNo        string         `gorm:"size:32;index;not null" json:"trade_no"`            // 原订单号
	MerchantID     int64          `gorm:"index;not null" json:"merchant_id"`                 // 商户ID
	OrderID        int64          `gorm:"index;not null" json:"order_id"`                    // 原订单ID
	ChannelID      int64          `gorm:"not null" json:"channel_id"`                        // 支付通道ID
	Amount         string         `gorm:"type:decimal(12,2);not null" json:"amount"`         // 退款金额
	RefundFee      string         `gorm:"type:decimal(12,2);default:0.00" json:"refund_fee"` // 退款手续费
	Reason         string         `gorm:"size:200" json:"reason"`                            // 退款原因
	Status         int8           `gorm:"default:0;index" json:"status"`                     // 状态：0待处理 1成功 2失败 3结果待确认
	ApiRefundNo    string         `gorm:"size:64" json:"api_refund_no"`                      // 第三方退款单号
	FailReason     string         `gorm:"size:200" json:"fail_reason"`                       // 失败原因
	NotifyURL      string         `gorm:"size:255" json:"notify_url"`                        // 异步通知地址
	NotifyStatus   int8           `gorm:"default:0" json:"notify_status"`                    // 通知状态：0未通知 1已通知
	NotifyCount    int            `gorm:"default:0" json:"notify_count"`                     // 通知次数
	NextNotifyTime *time.Time     `json:"next_notify_time"`                                  // 下次通知时间
	ProcessedAt    *time.Time     `json:"processed_at"`                                      // 处理完成时间
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

// 退款状态常量
const (
	RefundStatusPending    = 0 // 待处理
	RefundStatusSuccess    = 1 // 成功
	RefundStatusFailed     = 2 // 失败
	RefundStatusProcessing = 3 // 上游处理中或结果待确认
)

// TableName 指定表名
func (Refund) TableName() string {
	return "refunds"
}
