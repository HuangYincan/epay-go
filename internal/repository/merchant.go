// internal/repository/merchant.go
package repository

import (
	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MerchantRepository struct {
	db *gorm.DB
}

func NewMerchantRepository() *MerchantRepository {
	return &MerchantRepository{db: database.Get()}
}

// Create 创建商户
func (r *MerchantRepository) Create(merchant *model.Merchant) error {
	return r.db.Create(merchant).Error
}

// GetByID 根据ID获取商户
func (r *MerchantRepository) GetByID(id int64) (*model.Merchant, error) {
	var merchant model.Merchant
	err := r.db.First(&merchant, id).Error
	if err != nil {
		return nil, err
	}
	return &merchant, nil
}

// GetByIDForUpdate 串行处理同一商户的余额变动，保证流水前后余额准确。
func (r *MerchantRepository) GetByIDForUpdate(tx *gorm.DB, id int64) (*model.Merchant, error) {
	var merchant model.Merchant
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&merchant, id).Error; err != nil {
		return nil, err
	}
	return &merchant, nil
}

// GetByUsername 根据用户名获取商户
func (r *MerchantRepository) GetByUsername(username string) (*model.Merchant, error) {
	var merchant model.Merchant
	err := r.db.Where("username = ?", username).First(&merchant).Error
	if err != nil {
		return nil, err
	}
	return &merchant, nil
}

// GetByAPIKey 根据API Key获取商户
func (r *MerchantRepository) GetByAPIKey(apiKey string) (*model.Merchant, error) {
	var merchant model.Merchant
	err := r.db.Where("api_key = ?", apiKey).First(&merchant).Error
	if err != nil {
		return nil, err
	}
	return &merchant, nil
}

// Update 更新商户
func (r *MerchantRepository) UpdateFields(id int64, fields map[string]interface{}) error {
	return r.db.Model(&model.Merchant{}).Where("id = ?", id).Updates(fields).Error
}

// UpdateBalance 更新余额 (使用事务)
func (r *MerchantRepository) UpdateBalance(tx *gorm.DB, id int64, amount decimal.Decimal) error {
	return tx.Model(&model.Merchant{}).Where("id = ?", id).
		Update("balance", gorm.Expr("balance + ?", amount)).Error
}

// List 分页查询商户列表
func (r *MerchantRepository) List(page, pageSize int, status *int8) ([]model.Merchant, int64, error) {
	var merchants []model.Merchant
	var total int64

	query := r.db.Model(&model.Merchant{})
	if status != nil {
		query = query.Where("status = ?", *status)
	}

	err := query.Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	err = query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&merchants).Error
	if err != nil {
		return nil, 0, err
	}

	return merchants, total, nil
}

// ExistsByUsername 检查用户名是否存在
func (r *MerchantRepository) ExistsByUsername(username string) (bool, error) {
	var count int64
	err := r.db.Model(&model.Merchant{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

// GetFirst 获取一个商户（按 id 升序），用于测试等场景
func (r *MerchantRepository) GetFirst() (*model.Merchant, error) {
	var merchant model.Merchant
	err := r.db.Order("id ASC").First(&merchant).Error
	if err != nil {
		return nil, err
	}
	return &merchant, nil
}
