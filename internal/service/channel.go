// internal/service/channel.go
package service

import (
	"encoding/json"
	"fmt"
	"github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/pkg/safehttp"
	"strings"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/repository"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ChannelService struct {
	repo *repository.ChannelRepository
}

func NewChannelService() *ChannelService {
	return &ChannelService{
		repo: repository.NewChannelRepository(),
	}
}

// CreateChannelRequest 创建通道请求
type CreateChannelRequest struct {
	Name        string                 `json:"name" binding:"required"`
	Plugin      string                 `json:"plugin" binding:"required"`
	PayTypes    string                 `json:"pay_types"`
	AppType     string                 `json:"app_type"` // 支持的接口类型
	Config      map[string]interface{} `json:"config"`
	CallbackURL string                 `json:"callback_url"` // 完整回调地址，留空则自动使用当前请求域名拼接
	Rate        decimal.Decimal        `json:"rate"`
	DailyLimit  decimal.Decimal        `json:"daily_limit"`
	Status      int8                   `json:"status"`
	Sort        int                    `json:"sort"`
}

// Create 创建通道
func (s *ChannelService) Create(req *CreateChannelRequest) (*model.Channel, error) {
	if req.Status != 0 && req.Status != 1 {
		return nil, fmt.Errorf("通道状态无效")
	}
	if err := validateChannel(req.Plugin, req.AppType, req.CallbackURL, req.Rate, req.DailyLimit); err != nil {
		return nil, err
	}
	// 验证 AppType 不能为空
	if req.AppType == "" {
		return nil, fmt.Errorf("请至少选择一个支付接口")
	}

	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return nil, err
	}

	channel := &model.Channel{
		Name:        req.Name,
		Plugin:      req.Plugin,
		PayTypes:    req.PayTypes,
		AppType:     req.AppType,
		Config:      configJSON,
		CallbackURL: req.CallbackURL,
		Rate:        req.Rate,
		DailyLimit:  req.DailyLimit,
		Status:      req.Status,
		Sort:        req.Sort,
	}

	if err := s.repo.Create(channel); err != nil {
		return nil, err
	}

	return channel, nil
}

// GetByID 根据ID获取通道
func (s *ChannelService) GetByID(id int64) (*model.Channel, error) {
	return s.repo.GetByID(id)
}

// UpdateChannelRequest 更新通道请求
type UpdateChannelRequest struct {
	Name        *string                `json:"name"`
	PayTypes    *string                `json:"pay_types"`
	AppType     *string                `json:"app_type"`
	Config      map[string]interface{} `json:"config"`
	CallbackURL *string                `json:"callback_url"`
	Rate        *decimal.Decimal       `json:"rate"`
	DailyLimit  *decimal.Decimal       `json:"daily_limit"`
	Status      *int8                  `json:"status"`
	Sort        *int                   `json:"sort"`
}

// Update preserves omitted fields and serializes changes against order creation.
func (s *ChannelService) Update(id int64, req *UpdateChannelRequest) error {
	return database.Get().Transaction(func(tx *gorm.DB) error {
		var channel model.Channel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&channel, id).Error; err != nil {
			return err
		}
		fields := map[string]any{}
		if req.Name != nil {
			channel.Name = *req.Name
			fields["name"] = channel.Name
		}
		if req.PayTypes != nil {
			channel.PayTypes = *req.PayTypes
			fields["pay_types"] = channel.PayTypes
		}
		if req.AppType != nil {
			channel.AppType = *req.AppType
			fields["app_type"] = channel.AppType
		}
		if req.CallbackURL != nil {
			channel.CallbackURL = *req.CallbackURL
			fields["callback_url"] = channel.CallbackURL
		}
		if req.Config != nil {
			data, err := json.Marshal(req.Config)
			if err != nil {
				return err
			}
			fields["config"] = data
		}
		if req.Rate != nil {
			channel.Rate = *req.Rate
			fields["rate"] = channel.Rate
		}
		if req.DailyLimit != nil {
			channel.DailyLimit = *req.DailyLimit
			fields["daily_limit"] = channel.DailyLimit
		}
		if req.Status != nil {
			channel.Status = *req.Status
			fields["status"] = channel.Status
		}
		if req.Sort != nil {
			channel.Sort = *req.Sort
			fields["sort"] = channel.Sort
		}
		if channel.Status != 0 && channel.Status != 1 {
			return fmt.Errorf("通道状态无效")
		}
		if err := validateChannel(channel.Plugin, channel.AppType, channel.CallbackURL, channel.Rate, channel.DailyLimit); err != nil {
			return err
		}
		if len(fields) == 0 {
			return nil
		}
		return tx.Model(&channel).Updates(fields).Error
	})
}

// Delete 删除通道
func (s *ChannelService) Delete(id int64) error {
	return s.repo.Delete(id)
}

// List 分页查询通道列表
func (s *ChannelService) List(page, pageSize int) ([]model.Channel, int64, error) {
	return s.repo.List(page, pageSize)
}

// ListEnabled 获取所有启用的通道
func (s *ChannelService) ListEnabled() ([]model.Channel, error) {
	return s.repo.ListEnabled()
}

// GetAvailableChannel 根据支付类型获取可用通道
func (s *ChannelService) GetAvailableChannel(payType string) (*model.Channel, error) {
	return s.repo.GetAvailableByPayType(payType)
}

func validateChannel(plugin, methods, callback string, rate, daily decimal.Decimal) error {
	if rate.IsNegative() || rate.GreaterThanOrEqual(decimal.NewFromInt(100)) || !rate.Equal(rate.Round(4)) {
		return fmt.Errorf("费率必须为0到100之间的百分数，最多四位小数")
	}
	if daily.IsNegative() || !daily.Equal(daily.Round(2)) || daily.GreaterThan(decimal.RequireFromString("9999999999.99")) {
		return fmt.Errorf("日限额格式错误")
	}
	if strings.TrimSpace(methods) == "" {
		return fmt.Errorf("请至少选择一个支付接口")
	}
	for _, method := range strings.Split(methods, ",") {
		if err := payment.CheckMethod(plugin, methods, method); err != nil {
			return err
		}
	}
	if callback != "" {
		if _, err := safehttp.ParseURL(callback); err != nil {
			return err
		}
	}
	return nil
}
