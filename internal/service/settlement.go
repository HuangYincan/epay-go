package service

import (
	"errors"
	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/utils"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SettlementService struct {
	settleRepo   *repository.SettlementRepository
	merchantRepo *repository.MerchantRepository
}

func NewSettlementService() *SettlementService {
	return &SettlementService{repository.NewSettlementRepository(), repository.NewMerchantRepository()}
}

type ApplyRequest struct {
	MerchantID  int64           `json:"-"`
	Amount      decimal.Decimal `json:"amount" binding:"required"`
	AccountType string          `json:"account_type" binding:"required,oneof=alipay bank"`
	AccountNo   string          `json:"account_no" binding:"required"`
	AccountName string          `json:"account_name" binding:"required"`
}

func (s *SettlementService) Apply(req *ApplyRequest) (*model.Settlement, error) {
	if err := validateMoney(req.Amount); err != nil {
		return nil, err
	}
	if req.Amount.LessThan(decimal.NewFromInt(10)) {
		return nil, errors.New("最小结算金额为10元")
	}
	var result model.Settlement
	err := database.Get().Transaction(func(tx *gorm.DB) error {
		m, err := s.merchantRepo.GetByIDForUpdate(tx, req.MerchantID)
		if err != nil {
			return err
		}
		if m.Status != 1 {
			return errors.New("商户已被禁用")
		}
		var count int64
		if err := tx.Model(&model.Settlement{}).Where("merchant_id = ? AND status IN ?", m.ID, []int8{model.SettleStatusPending, model.SettleStatusProcessing}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("您有待处理的结算申请，请等待处理完成")
		}
		if m.Balance.LessThan(req.Amount) {
			return errors.New("余额不足")
		}
		fee := req.Amount.Mul(decimal.RequireFromString("0.02")).Round(2)
		result = model.Settlement{SettleNo: utils.GenerateSettleNo(), MerchantID: m.ID, Amount: req.Amount, Fee: fee, ActualAmount: req.Amount.Sub(fee), AccountType: req.AccountType, AccountNo: req.AccountNo, AccountName: req.AccountName, Status: model.SettleStatusPending}
		before := m.Balance
		after := before.Sub(req.Amount)
		if err := tx.Model(m).Updates(map[string]any{"balance": after, "frozen_balance": m.FrozenBalance.Add(req.Amount)}).Error; err != nil {
			return err
		}
		if err := repository.AddBalanceRecord(tx, m.ID, model.RecordActionExpense, req.Amount, before, after, "settle_freeze", result.SettleNo); err != nil {
			return err
		}
		return tx.Create(&result).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
func lockedSettlement(tx *gorm.DB, id int64) (*model.Settlement, error) {
	var v model.Settlement
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&v, id).Error
	return &v, err
}
func (s *SettlementService) Approve(id int64) error {
	return database.Get().Transaction(func(tx *gorm.DB) error {
		v, err := lockedSettlement(tx, id)
		if err != nil {
			return err
		}
		if v.Status == model.SettleStatusProcessing || v.Status == model.SettleStatusCompleted {
			return nil
		}
		if v.Status != model.SettleStatusPending {
			return errors.New("结算状态不正确")
		}
		return tx.Model(v).Updates(map[string]any{"status": model.SettleStatusProcessing, "remark": "审核通过，等待实际打款"}).Error
	})
}
func (s *SettlementService) Complete(id int64) error {
	return database.Get().Transaction(func(tx *gorm.DB) error {
		v, err := lockedSettlement(tx, id)
		if err != nil {
			return err
		}
		if v.Status == model.SettleStatusCompleted {
			return nil
		}
		if v.Status != model.SettleStatusProcessing {
			return errors.New("结算状态不正确")
		}
		m, err := s.merchantRepo.GetByIDForUpdate(tx, v.MerchantID)
		if err != nil {
			return err
		}
		if m.FrozenBalance.LessThan(v.Amount) {
			return errors.New("冻结余额不足，请核对账务")
		}
		if err := tx.Model(m).Update("frozen_balance", m.FrozenBalance.Sub(v.Amount)).Error; err != nil {
			return err
		}
		if err := tx.Model(v).Updates(map[string]any{"status": model.SettleStatusCompleted, "remark": "管理员已确认实际打款完成"}).Error; err != nil {
			return err
		}
		// Available funds were already deducted when frozen. Completion must not deduct them again.
		return repository.AddBalanceRecord(tx, m.ID, model.RecordActionExpense, decimal.Zero, m.Balance, m.Balance, "settle_complete", v.SettleNo)
	})
}
func (s *SettlementService) Reject(id int64, remark string) error {
	return database.Get().Transaction(func(tx *gorm.DB) error {
		v, err := lockedSettlement(tx, id)
		if err != nil {
			return err
		}
		if v.Status == model.SettleStatusRejected {
			return nil
		}
		if v.Status != model.SettleStatusPending {
			return errors.New("结算状态不正确")
		}
		m, err := s.merchantRepo.GetByIDForUpdate(tx, v.MerchantID)
		if err != nil {
			return err
		}
		if m.FrozenBalance.LessThan(v.Amount) {
			return errors.New("冻结余额不足，请核对账务")
		}
		before := m.Balance
		after := before.Add(v.Amount)
		if err := tx.Model(m).Updates(map[string]any{"balance": after, "frozen_balance": m.FrozenBalance.Sub(v.Amount)}).Error; err != nil {
			return err
		}
		if err := tx.Model(v).Updates(map[string]any{"status": model.SettleStatusRejected, "remark": remark}).Error; err != nil {
			return err
		}
		return repository.AddBalanceRecord(tx, m.ID, model.RecordActionIncome, v.Amount, before, after, "settle_unfreeze", v.SettleNo)
	})
}
func (s *SettlementService) List(page, pageSize int, merchantID *int64, status *int8) ([]model.Settlement, int64, error) {
	return s.settleRepo.List(page, pageSize, merchantID, status)
}
func (s *SettlementService) GetByID(id int64) (*model.Settlement, error) {
	return s.settleRepo.GetByID(id)
}
