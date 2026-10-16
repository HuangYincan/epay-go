// Package accounting contains the transaction-local income operation shared by
// provider callbacks and authenticated observer evidence settlement.
package accounting

import (
	"errors"
	"time"

	"github.com/example/epay-go/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreditLockedOrder requires the caller to hold the original order FOR UPDATE
// and establish its payment evidence. It never opens or commits a transaction.
// A nil provider reference means absent evidence, not an invented transaction ID.
func CreditLockedOrder(tx *gorm.DB, order *model.Order, providerReference *string, buyer string, now time.Time) (int64, error) {
	if order.Status != model.OrderStatusUnpaid || !order.Amount.IsPositive() || order.Fee.IsNegative() || order.Fee.GreaterThan(order.Amount) {
		return 0, errors.New("订单状态或收入金额无效")
	}
	var merchant model.Merchant
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&merchant, order.MerchantID).Error; err != nil {
		return 0, err
	}
	income := order.Amount.Sub(order.Fee)
	before := merchant.Balance
	after := before.Add(income)
	updates := map[string]any{"status": model.OrderStatusPaid, "paid_at": now, "api_trade_no": providerReference, "buyer": buyer,
		"notify_status": model.NotifyStatusPending, "notify_count": 0, "next_notify_at": nil, "notify_lease_until": nil, "notify_lease_token": "", "next_query_at": nil}
	result := tx.Model(order).Where("status = ?", model.OrderStatusUnpaid).Updates(updates)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, errors.New("订单已被处理")
	}
	if err := tx.Model(&merchant).Update("balance", after).Error; err != nil {
		return 0, err
	}
	record := model.BalanceRecord{MerchantID: merchant.ID, Action: model.RecordActionIncome, Amount: income, BeforeBalance: before, AfterBalance: after, Type: "order_income", TradeNo: order.TradeNo}
	if err := tx.Create(&record).Error; err != nil {
		return 0, err
	}
	return record.ID, nil
}
