// internal/repository/order.go
package repository

import (
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OrderRepository struct {
	db *gorm.DB
}

func NewOrderRepository() *OrderRepository {
	return &OrderRepository{db: database.Get()}
}

// Create 创建订单
func (r *OrderRepository) Create(order *model.Order) error {
	return r.db.Create(order).Error
}

// GetByID 根据ID获取订单
func (r *OrderRepository) GetByID(id int64) (*model.Order, error) {
	var order model.Order
	err := r.db.Preload("Merchant").Preload("Channel").First(&order, id).Error
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// GetByTradeNo 根据系统订单号获取订单
func (r *OrderRepository) GetByTradeNo(tradeNo string) (*model.Order, error) {
	var order model.Order
	err := r.db.Preload("Merchant").Preload("Channel", func(db *gorm.DB) *gorm.DB { return db.Unscoped() }).
		Where("trade_no = ?", tradeNo).First(&order).Error
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// GetByTradeNoForUpdate 在支付事务内锁定订单；关联信息由调用方按需查询。
func (r *OrderRepository) GetByTradeNoForUpdate(tx *gorm.DB, tradeNo string) (*model.Order, error) {
	var order model.Order
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// GetByOutTradeNo 根据商户订单号获取订单
func (r *OrderRepository) GetByOutTradeNo(merchantID int64, outTradeNo string) (*model.Order, error) {
	var order model.Order
	err := r.db.Where("merchant_id = ? AND out_trade_no = ?", merchantID, outTradeNo).
		First(&order).Error
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// Update 更新订单
func (r *OrderRepository) Update(order *model.Order) error {
	return r.db.Save(order).Error
}

// UpdateStatus 更新订单状态
func (r *OrderRepository) UpdateStatus(tradeNo string, status int8) error {
	updates := map[string]interface{}{"status": status}
	if status == model.OrderStatusPaid {
		now := time.Now()
		updates["paid_at"] = &now
	}
	return r.db.Model(&model.Order{}).Where("trade_no = ?", tradeNo).Updates(updates).Error
}

// ClaimNotify serializes delivery across callbacks, workers and app instances.
// An expired lease can be reclaimed after a crash; only its current owner may finish.
func (r *OrderRepository) ClaimNotify(tradeNo, token string, now, leaseUntil time.Time, force bool) (*model.Order, error) {
	var claimed *model.Order
	err := r.db.Transaction(func(tx *gorm.DB) error {
		order, err := r.GetByTradeNoForUpdate(tx, tradeNo)
		if err != nil {
			return err
		}
		if order.Status != model.OrderStatusPaid || (!force && order.NotifyStatus >= model.NotifyStatusSuccess) ||
			(!force && order.NextNotifyAt != nil && order.NextNotifyAt.After(now)) ||
			(order.NotifyLeaseUntil != nil && order.NotifyLeaseUntil.After(now)) {
			return nil
		}
		count := order.NotifyCount + 1
		if force {
			count = 1 // An explicit admin resend starts a new delivery cycle.
		}
		if err := tx.Model(order).Updates(map[string]any{
			"notify_status": model.NotifyStatusSending, "notify_count": count,
			"notify_lease_until": leaseUntil, "notify_lease_token": token, "next_notify_at": nil,
		}).Error; err != nil {
			return err
		}
		claimed = order
		return nil
	})
	return claimed, err
}

func (r *OrderRepository) FinishNotify(tradeNo, token string, status int8, nextNotifyAt *time.Time) error {
	return r.db.Model(&model.Order{}).
		Where("trade_no = ? AND notify_status = ? AND notify_lease_token = ?", tradeNo, model.NotifyStatusSending, token).
		Updates(map[string]any{"notify_status": status, "next_notify_at": nextNotifyAt,
			"notify_lease_until": nil, "notify_lease_token": ""}).Error
}

// UpdatePayInfo 在调用方的事务内更新支付信息。
func (r *OrderRepository) UpdatePayInfo(tx *gorm.DB, tradeNo, apiTradeNo, buyer string) error {
	now := time.Now()
	return tx.Model(&model.Order{}).Where("trade_no = ?", tradeNo).Updates(map[string]interface{}{
		"api_trade_no": apiTradeNo,
		"buyer":        buyer,
		"status":       model.OrderStatusPaid,
		"paid_at":      &now,
	}).Error
}

// List 分页查询订单列表
func (r *OrderRepository) List(page, pageSize int, merchantID *int64, status *int8) ([]model.Order, int64, error) {
	var orders []model.Order
	var total int64

	query := r.db.Model(&model.Order{})
	if merchantID != nil {
		query = query.Where("merchant_id = ?", *merchantID)
	}
	if status != nil {
		query = query.Where("status = ?", *status)
	}

	err := query.Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	err = query.Preload("Merchant").Preload("Channel").
		Offset(offset).Limit(pageSize).Order("id DESC").Find(&orders).Error
	if err != nil {
		return nil, 0, err
	}

	return orders, total, nil
}

// GetPendingNotifyOrders 获取待通知的订单
func (r *OrderRepository) GetPendingNotifyOrders(limit int) ([]model.Order, error) {
	var orders []model.Order
	err := r.db.Where("status = ? AND notify_status < ? AND (next_notify_at IS NULL OR next_notify_at <= ?)",
		model.OrderStatusPaid, model.NotifyStatusSuccess, time.Now()).
		Where("notify_lease_until IS NULL OR notify_lease_until <= ?", time.Now()).
		Limit(limit).Find(&orders).Error
	return orders, err
}

// GetPendingQueryOrders 获取待主动查询的未支付订单
func (r *OrderRepository) GetPendingQueryOrders(limit int) ([]model.Order, error) {
	var orders []model.Order
	err := r.db.Where("status = ? AND next_query_at IS NOT NULL AND next_query_at <= ?",
		model.OrderStatusUnpaid, time.Now()).
		Limit(limit).Find(&orders).Error
	return orders, err
}

// UpdateQueryStatus 更新主动查询进度
func (r *OrderRepository) UpdateQueryStatus(tradeNo string, nextQueryAt *time.Time, expectedCount int) error {
	updates := map[string]interface{}{"query_count": gorm.Expr("query_count + 1")}
	if nextQueryAt != nil {
		updates["next_query_at"] = nextQueryAt
	} else {
		updates["next_query_at"] = gorm.Expr("NULL")
	}
	// A stale worker must not clear the schedule restarted by an H5 refresh,
	// or advance the same attempt twice across application instances.
	return r.db.Model(&model.Order{}).Where("trade_no = ? AND query_count = ?", tradeNo, expectedCount).Updates(updates).Error
}

// GetTodayStats 获取今日统计
func (r *OrderRepository) GetTodayStats(merchantID *int64) (int64, decimal.Decimal, error) {
	var count int64

	today := time.Now().Format("2006-01-02")
	query := r.db.Model(&model.Order{}).
		Where("status = ? AND DATE(created_at) = ?", model.OrderStatusPaid, today)

	if merchantID != nil {
		query = query.Where("merchant_id = ?", *merchantID)
	}

	err := query.Count(&count).Error
	if err != nil {
		return 0, decimal.Zero, err
	}

	var result struct {
		Total decimal.Decimal
	}
	err = query.Select("COALESCE(SUM(amount), 0) as total").Scan(&result).Error
	if err != nil {
		return 0, decimal.Zero, err
	}

	return count, result.Total, nil
}
