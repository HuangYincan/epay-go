package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/utils"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"time"
)

type RefundService struct {
	refundRepo   *repository.RefundRepository
	orderRepo    *repository.OrderRepository
	merchantRepo *repository.MerchantRepository
}

func NewRefundService() *RefundService {
	return &RefundService{repository.NewRefundRepository(), repository.NewOrderRepository(), repository.NewMerchantRepository()}
}

type CreateRefundRequest struct {
	TradeNo   string `json:"trade_no" binding:"required"`
	Amount    string `json:"amount" binding:"required"`
	Reason    string `json:"reason"`
	NotifyURL string `json:"notify_url"`
}

func (s *RefundService) create(merchantID *int64, req *CreateRefundRequest) (*model.Refund, error) {
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		return nil, errors.New("退款金额格式错误")
	}
	if err = validateMoney(amount); err != nil {
		return nil, err
	}
	var result model.Refund
	err = database.Get().Transaction(func(tx *gorm.DB) error {
		order, err := s.orderRepo.GetByTradeNoForUpdate(tx, req.TradeNo)
		if err != nil {
			return err
		}
		if merchantID != nil && order.MerchantID != *merchantID {
			return errors.New("无权操作此订单")
		}
		if order.Status != model.OrderStatusPaid {
			return errors.New("订单未支付或已全额退款")
		}
		// Include pending/uncertain requests to prevent concurrent approvals exceeding the paid amount.
		var total decimal.Decimal
		if err := tx.Model(&model.Refund{}).Select("COALESCE(SUM(amount),0)").Where("trade_no = ? AND status <> ?", order.TradeNo, model.RefundStatusFailed).Scan(&total).Error; err != nil {
			return err
		}
		if total.Add(amount).GreaterThan(order.Amount) {
			return errors.New("退款申请总额超过订单金额")
		}
		result = model.Refund{RefundNo: utils.GenerateRefundNo(), TradeNo: order.TradeNo, MerchantID: order.MerchantID, OrderID: order.ID, ChannelID: order.ChannelID, Amount: amount.StringFixed(2), RefundFee: "0.00", Reason: req.Reason, NotifyURL: req.NotifyURL, Status: model.RefundStatusPending}
		return tx.Create(&result).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func (s *RefundService) CreateRefund(id int64, req *CreateRefundRequest) (*model.Refund, error) {
	return s.create(&id, req)
}
func (s *RefundService) CreateRefundByAdmin(req *CreateRefundRequest) (*model.Refund, error) {
	return s.create(nil, req)
}

// Lock order -> refund -> merchant, matching payment's order -> merchant lock order.
func (s *RefundService) locked(tx *gorm.DB, no string) (*model.Order, *model.Refund, error) {
	var hint model.Refund
	if err := tx.Where("refund_no = ?", no).First(&hint).Error; err != nil {
		return nil, nil, err
	}
	order, err := s.orderRepo.GetByTradeNoForUpdate(tx, hint.TradeNo)
	if err != nil {
		return nil, nil, err
	}
	var refund model.Refund
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_no = ?", no).First(&refund).Error
	return order, &refund, err
}
func (s *RefundService) ProcessRefund(no string, approve bool, reason string) error {
	var request *payment.RefundRequest
	var channel model.Channel
	claimed := false
	err := database.Get().Transaction(func(tx *gorm.DB) error {
		order, r, err := s.locked(tx, no)
		if err != nil {
			return err
		}
		if r.Status == model.RefundStatusSuccess {
			return nil
		}
		if r.Status == model.RefundStatusProcessing {
			return nil
		}
		if r.Status != model.RefundStatusPending {
			return errors.New("退款单已处理")
		}
		if !approve {
			return tx.Model(r).Updates(map[string]any{"status": model.RefundStatusFailed, "fail_reason": reason, "processed_at": time.Now()}).Error
		}
		if order.Status != model.OrderStatusPaid {
			return errors.New("订单状态不允许退款")
		}
		amount, err := decimal.NewFromString(r.Amount)
		if err != nil {
			return err
		}
		if err = validateMoney(amount); err != nil {
			return err
		}
		if err = tx.First(&channel, order.ChannelID).Error; err != nil {
			return err
		}
		merchant, err := s.merchantRepo.GetByIDForUpdate(tx, r.MerchantID)
		if err != nil {
			return err
		}
		if merchant.Balance.LessThan(amount) {
			return errors.New("商户余额不足，无法执行退款")
		}
		before := merchant.Balance
		after := before.Sub(amount)
		if err = tx.Model(merchant).Updates(map[string]any{"balance": after, "frozen_balance": merchant.FrozenBalance.Add(amount)}).Error; err != nil {
			return err
		}
		if err = repository.AddBalanceRecord(tx, merchant.ID, model.RecordActionExpense, amount, before, after, "refund_freeze", no); err != nil {
			return err
		}
		if err = tx.Model(r).Updates(map[string]any{"status": model.RefundStatusProcessing, "funds_reserved": true, "next_query_at": time.Now().Add(time.Minute)}).Error; err != nil {
			return err
		}
		request = &payment.RefundRequest{TradeNo: order.TradeNo, RefundNo: no, TotalAmount: order.Amount, Amount: amount, RefundDesc: r.Reason}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	adapter, err := payment.NewAdapter(channel.Plugin, channel.Config)
	if err != nil {
		return s.deferQuery(no, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := adapter.Refund(ctx, request)
	// A timeout is uncertain, not a rejection: retain funds until an authoritative query resolves it.
	if err != nil {
		return s.deferQuery(no, err)
	}
	return s.finish(no, result)
}
func (s *RefundService) deferQuery(no string, cause error) error {
	err := database.Get().Model(&model.Refund{}).Where("refund_no = ? AND status = ?", no, model.RefundStatusProcessing).Updates(map[string]any{"fail_reason": "上游结果待确认", "next_query_at": time.Now().Add(time.Minute)}).Error
	if err != nil {
		return err
	}
	return fmt.Errorf("退款结果待确认，将自动查询: %w", cause)
}
func (s *RefundService) finish(no string, result *payment.RefundResponse) error {
	if result == nil {
		return errors.New("退款响应为空")
	}
	return database.Get().Transaction(func(tx *gorm.DB) error {
		order, r, err := s.locked(tx, no)
		if err != nil {
			return err
		}
		if r.Status == model.RefundStatusSuccess || r.Status == model.RefundStatusFailed {
			return nil
		}
		if r.Status != model.RefundStatusProcessing || !r.FundsReserved {
			return errors.New("退款资金尚未预留")
		}
		if result.Status != "success" && result.Status != "failed" {
			return tx.Model(r).Updates(map[string]any{"api_refund_no": result.ApiRefundNo, "next_query_at": time.Now().Add(time.Minute)}).Error
		}
		amount, err := decimal.NewFromString(r.Amount)
		if err != nil {
			return err
		}
		m, err := s.merchantRepo.GetByIDForUpdate(tx, r.MerchantID)
		if err != nil {
			return err
		}
		if m.FrozenBalance.LessThan(amount) {
			return errors.New("退款冻结金额不足，请核对账务")
		}
		before := m.Balance
		after := before
		action := int8(model.RecordActionExpense)
		ledgerAmount := decimal.Zero
		kind := "refund"
		status := int8(model.RefundStatusSuccess)
		if result.Status == "failed" {
			status = model.RefundStatusFailed
			after = after.Add(amount)
			action = model.RecordActionIncome
			ledgerAmount = amount
			kind = "refund_unfreeze"
		}
		if err = tx.Model(m).Updates(map[string]any{"balance": after, "frozen_balance": m.FrozenBalance.Sub(amount)}).Error; err != nil {
			return err
		}
		if err = tx.Model(r).Updates(map[string]any{"status": status, "api_refund_no": result.ApiRefundNo, "fail_reason": result.ErrorMessage, "funds_reserved": false, "next_query_at": nil, "processed_at": time.Now()}).Error; err != nil {
			return err
		}
		if status == model.RefundStatusSuccess {
			var total decimal.Decimal
			if err = tx.Model(&model.Refund{}).Select("COALESCE(SUM(amount),0)").Where("trade_no = ? AND status = ?", order.TradeNo, model.RefundStatusSuccess).Scan(&total).Error; err != nil {
				return err
			}
			if total.Equal(order.Amount) {
				if err = tx.Model(order).Update("status", model.OrderStatusRefund).Error; err != nil {
					return err
				}
			}
		}
		// The available balance was deducted at reservation, so settlement does not deduct it again.
		return repository.AddBalanceRecord(tx, m.ID, action, ledgerAmount, before, after, kind, no)
	})
}
func (s *RefundService) Reconcile(ctx context.Context, no string) error {
	var r model.Refund
	if err := database.Get().Where("refund_no = ?", no).First(&r).Error; err != nil {
		return err
	}
	if r.Status != model.RefundStatusProcessing {
		return nil
	}
	// Persistent lease prevents concurrent workers from retrying the same request.
	now := time.Now()
	claimed := database.Get().Model(&model.Refund{}).Where("id = ? AND status = ? AND (next_query_at IS NULL OR next_query_at <= ?)", r.ID, model.RefundStatusProcessing, now).Update("next_query_at", now.Add(5*time.Minute))
	if claimed.Error != nil {
		return claimed.Error
	}
	if claimed.RowsAffected == 0 {
		return nil
	}
	order, err := s.orderRepo.GetByTradeNo(r.TradeNo)
	if err != nil {
		return err
	}
	if order.Channel == nil {
		return errors.New("退款通道不存在")
	}
	adapter, err := payment.NewAdapter(order.Channel.Plugin, order.Channel.Config)
	if err != nil {
		return err
	}
	querier, ok := adapter.(payment.RefundQuerier)
	if !ok {
		return errors.New("支付适配器不支持退款查询")
	}
	amount, err := decimal.NewFromString(r.Amount)
	if err != nil {
		return err
	}
	request := &payment.RefundRequest{TradeNo: r.TradeNo, RefundNo: no, TotalAmount: order.Amount, Amount: amount, RefundDesc: r.Reason}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := querier.QueryRefund(ctx, request)
	if err != nil {
		return s.deferQuery(no, err)
	}
	if result == nil {
		return errors.New("退款查询响应为空")
	}
	if result.Status == "success" && !result.Amount.Equal(amount) {
		return errors.New("上游退款金额不匹配")
	}
	if result.Status == "not_found" {
		result, err = adapter.Refund(ctx, request)
		if err != nil {
			return s.deferQuery(no, err)
		}
	}
	if result != nil && result.Status == "success" && !result.Amount.IsZero() && !result.Amount.Equal(amount) {
		return errors.New("上游退款金额不匹配")
	}
	return s.finish(no, result)
}
func (s *RefundService) StartWorker(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var refunds []model.Refund
			if err := database.Get().Where("status = ? AND (next_query_at IS NULL OR next_query_at <= ?)", model.RefundStatusProcessing, time.Now()).Limit(30).Find(&refunds).Error; err != nil {
				log.Printf("Refund queue: %v", err)
				continue
			}
			for _, r := range refunds {
				if err := s.Reconcile(ctx, r.RefundNo); err != nil {
					log.Printf("Refund reconciliation %s: %v", r.RefundNo, err)
				}
			}
		}
	}
}
func (s *RefundService) GetRefundByNo(no string) (*model.Refund, error) {
	return s.refundRepo.GetByRefundNo(no)
}
func (s *RefundService) List(page, size int, id *int64, status *int8) ([]*model.Refund, int64, error) {
	return s.refundRepo.List(page, size, id, status)
}
