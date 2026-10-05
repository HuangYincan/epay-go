// internal/service/merchant.go
package service

import (
	"errors"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/utils"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type MerchantService struct {
	repo *repository.MerchantRepository
}

func NewMerchantService() *MerchantService {
	return &MerchantService{
		repo: repository.NewMerchantRepository(),
	}
}

// RegisterRequest 注册请求
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=4,max=32"`
	Password string `json:"password" binding:"required,min=6,max=32"`
	Email    string `json:"email" binding:"omitempty,email"`
	Phone    string `json:"phone" binding:"omitempty"`
}

// Register 商户注册
func (s *MerchantService) Register(req *RegisterRequest) (*model.Merchant, error) {
	// 检查用户名是否存在
	exists, err := s.repo.ExistsByUsername(req.Username)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New("用户名已存在")
	}

	// 加密密码
	hashedPassword, err := utils.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	// 创建商户
	merchant := &model.Merchant{
		Username: req.Username,
		Password: hashedPassword,
		Email:    req.Email,
		Phone:    req.Phone,
		ApiKey:   utils.GenerateAPIKey(),
		Balance:  decimal.Zero,
		Status:   1,
	}

	if err := s.repo.Create(merchant); err != nil {
		return nil, err
	}

	return merchant, nil
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 商户登录
func (s *MerchantService) Login(req *LoginRequest) (*model.Merchant, error) {
	merchant, err := s.repo.GetByUsername(req.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("用户名或密码错误")
		}
		return nil, err
	}

	if merchant.Status != 1 {
		return nil, errors.New("账号已被禁用")
	}

	if !utils.CheckPassword(req.Password, merchant.Password) {
		return nil, errors.New("用户名或密码错误")
	}

	return merchant, nil
}

// GetByID 根据ID获取商户
func (s *MerchantService) GetByID(id int64) (*model.Merchant, error) {
	return s.repo.GetByID(id)
}

// GetByAPIKey 根据API Key获取商户
func (s *MerchantService) GetByAPIKey(apiKey string) (*model.Merchant, error) {
	return s.repo.GetByAPIKey(apiKey)
}

// List 分页查询商户列表
func (s *MerchantService) List(page, pageSize int, status *int8) ([]model.Merchant, int64, error) {
	return s.repo.List(page, pageSize, status)
}

// UpdateStatus 更新商户状态
func (s *MerchantService) UpdateStatus(id int64, status int8) error {
	merchant, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	return s.repo.UpdateFields(merchant.ID, map[string]interface{}{"status": status})
}

// ResetAPIKey 重置API密钥
func (s *MerchantService) ResetAPIKey(id int64) (string, error) {
	merchant, err := s.repo.GetByID(id)
	if err != nil {
		return "", err
	}
	merchant.ApiKey = utils.GenerateAPIKey()
	if err := s.repo.UpdateFields(id, map[string]interface{}{"api_key": merchant.ApiKey}); err != nil {
		return "", err
	}
	return merchant.ApiKey, nil
}

// UpdatePassword 更新密码
func (s *MerchantService) UpdatePassword(id int64, oldPassword, newPassword string) error {
	merchant, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}

	if !utils.CheckPassword(oldPassword, merchant.Password) {
		return errors.New("原密码错误")
	}

	hashedPassword, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}

	merchant.Password = hashedPassword
	return s.repo.UpdateFields(id, map[string]interface{}{"password": hashedPassword})
}

func (s *MerchantService) UpdateProfile(id int64, email, phone string) error {
	return s.repo.UpdateFields(id, map[string]interface{}{"email": email, "phone": phone})
}
