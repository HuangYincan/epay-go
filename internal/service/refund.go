// internal/service/refund.go
package service

import (
	"context"
	"errors"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/utils"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type RefundService struct {
	refundRepo   *repository.RefundRepository
	orderRepo    *repository.OrderRepository
	merchantRepo *repository.MerchantRepository
	recordRepo   *repository.BalanceRecordRepository
}

func NewRefundService() *RefundService {
	return &RefundService{
		refundRepo:   repository.NewRefundRepository(),
		orderRepo:    repository.NewOrderRepository(),
		merchantRepo: repository.NewMerchantRepository(),
		recordRepo:   repository.NewBalanceRecordRepository(),
	}
}

// CreateRefundRequest 创建退款请求
type CreateRefundRequest struct {
	TradeNo   string `json:"trade_no" binding:"required"`
	Amount    string `json:"amount" binding:"required"`
	Reason    string `json:"reason"`
	NotifyURL string `json:"notify_url"`
}

func (s *RefundService) createRefundForOrder(order *model.Order, req *CreateRefundRequest) (*model.Refund, error) {
	if order.Status != model.OrderStatusPaid {
		return nil, errors.New("订单未支付，无法退款")
	}

	refundAmount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		return nil, errors.New("退款金额格式错误")
	}
	if refundAmount.GreaterThan(order.Amount) {
		return nil, errors.New("退款金额不能大于订单金额")
	}

	existingRefunds, err := s.refundRepo.GetByTradeNo(req.TradeNo)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	totalRefunded := decimal.Zero
	for _, r := range existingRefunds {
		if r.Status == model.RefundStatusSuccess {
			amt, _ := decimal.NewFromString(r.Amount)
			totalRefunded = totalRefunded.Add(amt)
		}
	}

	if totalRefunded.Add(refundAmount).GreaterThan(order.Amount) {
		return nil, errors.New("退款总额超过订单金额")
	}

	refund := &model.Refund{
		RefundNo:   utils.GenerateRefundNo(),
		TradeNo:    order.TradeNo,
		MerchantID: order.MerchantID,
		OrderID:    order.ID,
		ChannelID:  order.ChannelID,
		Amount:     req.Amount,
		RefundFee:  "0.00",
		Reason:     req.Reason,
		Status:     model.RefundStatusPending,
		NotifyURL:  req.NotifyURL,
	}

	if err := s.refundRepo.Create(refund); err != nil {
		return nil, err
	}

	return refund, nil
}

// CreateRefund 创建退款单
func (s *RefundService) CreateRefund(merchantID int64, req *CreateRefundRequest) (*model.Refund, error) {
	// 查询原订单
	order, err := s.orderRepo.GetByTradeNo(req.TradeNo)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("订单不存在")
		}
		return nil, err
	}

	// 验证商户权限
	if order.MerchantID != merchantID {
		return nil, errors.New("无权操作此订单")
	}

	return s.createRefundForOrder(order, req)
}

// CreateRefundByAdmin 管理员创建退款单
func (s *RefundService) CreateRefundByAdmin(req *CreateRefundRequest) (*model.Refund, error) {
	order, err := s.orderRepo.GetByTradeNo(req.TradeNo)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("订单不存在")
		}
		return nil, err
	}

	return s.createRefundForOrder(order, req)
}

// ProcessRefund 处理退款（管理员审核）
func (s *RefundService) ProcessRefund(refundNo string, success bool, failReason string) error {
	refund, err := s.refundRepo.GetByRefundNo(refundNo)
	if err != nil {
		return err
	}

	if refund.Status != model.RefundStatusPending {
		return errors.New("退款单已处理")
	}

	now := time.Now()
	if success {
		order, err := s.orderRepo.GetByTradeNo(refund.TradeNo)
		if err != nil {
			return err
		}
		if order.Status != model.OrderStatusPaid {
			return errors.New("订单状态不允许退款")
		}
		if order.Channel == nil {
			return errors.New("订单支付通道不存在")
		}

		amount, _ := decimal.NewFromString(refund.Amount)
		merchant, err := s.merchantRepo.GetByID(refund.MerchantID)
		if err != nil {
			return err
		}
		if merchant.Balance.LessThan(amount) {
			return errors.New("商户余额不足，无法执行退款")
		}

		adapter, err := payment.NewAdapter(order.Channel.Plugin, order.Channel.Config)
		if err != nil {
			return err
		}

		refundResp, err := adapter.Refund(context.Background(), &payment.RefundRequest{
			TradeNo:     order.TradeNo,
			RefundNo:    refund.RefundNo,
			TotalAmount: order.Amount,
			Amount:      amount,
			RefundDesc:  refund.Reason,
		})
		if err != nil {
			refund.Status = model.RefundStatusFailed
			refund.FailReason = err.Error()
			refund.ProcessedAt = &now
			return s.refundRepo.Update(refund)
		}
		if refundResp.Status != "success" && refundResp.Status != "processing" {
			refund.Status = model.RefundStatusFailed
			refund.FailReason = refundResp.ErrorMessage
			refund.ProcessedAt = &now
			return s.refundRepo.Update(refund)
		}

		refund.Status = model.RefundStatusSuccess
		refund.ApiRefundNo = refundResp.ApiRefundNo
		refund.FailReason = ""
		refund.ProcessedAt = &now

		beforeBalance := merchant.Balance
		afterBalance := beforeBalance.Sub(amount)

		tx := database.Get().Begin()
		defer func() {
			if r := recover(); r != nil {
				tx.Rollback()
			}
		}()

		if err := s.refundRepo.Update(refund); err != nil {
			tx.Rollback()
			return err
		}

		if err := s.orderRepo.UpdateStatus(refund.TradeNo, model.OrderStatusRefund); err != nil {
			tx.Rollback()
			return err
		}

		if err := s.merchantRepo.UpdateBalance(tx, merchant.ID, amount.Neg()); err != nil {
			tx.Rollback()
			return err
		}

		record := &model.BalanceRecord{
			MerchantID:    refund.MerchantID,
			Action:        model.RecordActionExpense,
			Amount:        amount,
			BeforeBalance: beforeBalance,
			AfterBalance:  afterBalance,
			Type:          "refund",
			TradeNo:       refund.RefundNo,
		}
		if err := s.recordRepo.CreateWithTx(tx, record); err != nil {
			tx.Rollback()
			return err
		}

		return tx.Commit().Error
	} else {
		refund.Status = model.RefundStatusFailed
		refund.FailReason = failReason
		refund.ProcessedAt = &now
	}

	return s.refundRepo.Update(refund)
}

// GetRefundByNo 根据退款单号查询
func (s *RefundService) GetRefundByNo(refundNo string) (*model.Refund, error) {
	return s.refundRepo.GetByRefundNo(refundNo)
}

// List 退款列表
func (s *RefundService) List(page, pageSize int, merchantID *int64, status *int8) ([]*model.Refund, int64, error) {
	return s.refundRepo.List(page, pageSize, merchantID, status)
}
