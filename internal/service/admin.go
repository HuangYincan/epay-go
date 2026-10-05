// internal/service/admin.go
package service

import (
	"errors"
	"os"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/utils"
	"gorm.io/gorm"
)

type AdminService struct {
	repo *repository.AdminRepository
}

func NewAdminService() *AdminService {
	return &AdminService{
		repo: repository.NewAdminRepository(),
	}
}

// InitDefaultAdmin 初始化默认管理员（如果不存在）
func (s *AdminService) InitDefaultAdmin() error {
	count, err := s.repo.Count()
	if err != nil {
		return err
	}

	if count > 0 {
		return nil // 已有管理员，跳过
	}

	// 创建默认管理员
	username := os.Getenv("DEFAULT_ADMIN_USERNAME")
	if username == "" {
		username = "epay_admin"
	}

	password := os.Getenv("DEFAULT_ADMIN_PASSWORD")
	if len(password) < 12 || password == "ChangeMe123!" {
		return errors.New("首次启动必须设置至少12字节的 DEFAULT_ADMIN_PASSWORD，不能使用示例密码")
	}

	hashedPassword, err := utils.HashPassword(password)
	if err != nil {
		return err
	}

	admin := &model.Admin{
		Username: username,
		Password: hashedPassword,
		Role:     "super",
	}

	return s.repo.Create(admin)
}

// AdminLoginRequest 管理员登录请求
type AdminLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 管理员登录
func (s *AdminService) Login(req *AdminLoginRequest) (*model.Admin, error) {
	admin, err := s.repo.GetByUsername(req.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("用户名或密码错误")
		}
		return nil, err
	}

	if !utils.CheckPassword(req.Password, admin.Password) {
		return nil, errors.New("用户名或密码错误")
	}

	// 更新最后登录时间
	_ = s.repo.UpdateLastLogin(admin.ID)

	return admin, nil
}

// GetByID 根据ID获取管理员
func (s *AdminService) GetByID(id int64) (*model.Admin, error) {
	return s.repo.GetByID(id)
}

// UpdatePassword 更新密码
func (s *AdminService) UpdatePassword(id int64, oldPassword, newPassword string) error {
	admin, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}

	if !utils.CheckPassword(oldPassword, admin.Password) {
		return errors.New("原密码错误")
	}

	hashedPassword, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}

	admin.Password = hashedPassword
	return s.repo.Update(admin)
}
