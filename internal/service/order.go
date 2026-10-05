// internal/service/order.go
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/safehttp"
	"github.com/example/epay-go/pkg/utils"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OrderService struct {
	orderRepo    *repository.OrderRepository
	channelRepo  *repository.ChannelRepository
	merchantRepo *repository.MerchantRepository
	recordRepo   *repository.BalanceRecordRepository
}

func NewOrderService() *OrderService {
	return &OrderService{
		orderRepo:    repository.NewOrderRepository(),
		channelRepo:  repository.NewChannelRepository(),
		merchantRepo: repository.NewMerchantRepository(),
		recordRepo:   repository.NewBalanceRecordRepository(),
	}
}

// CreateOrderRequest 创建订单请求
type CreateOrderRequest struct {
	ChannelID         int64             `json:"-"`
	MerchantID        int64             `json:"-"`
	OutTradeNo        string            `json:"out_trade_no" binding:"required"`
	Amount            decimal.Decimal   `json:"money" binding:"required"`
	Name              string            `json:"name" binding:"required"`
	PayType           string            `json:"type" binding:"required"` // alipay, wxpay
	NotifyURL         string            `json:"notify_url" binding:"omitempty,url"`
	MerchantNotifyURL string            `json:"-"`
	PlatformBaseURL   string            `json:"-"`
	ReturnURL         string            `json:"return_url" binding:"omitempty,url"`
	ClientIP          string            `json:"-"`
	PayMethod         string            `json:"pay_method"` // scan, h5, jsapi, web
	Extra             map[string]string `json:"extra"`
}

// CreateOrderResponse 创建订单响应
type CreateOrderResponse struct {
	TradeNo   string `json:"trade_no"`
	PayType   string `json:"pay_type"`
	PayURL    string `json:"pay_url,omitempty"`
	PayParams string `json:"pay_params,omitempty"`
}

// Create 创建订单
func (s *OrderService) Create(ctx context.Context, req *CreateOrderRequest) (*CreateOrderResponse, error) {
	if err := validateMoney(req.Amount); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.OutTradeNo) == "" || len(req.OutTradeNo) > 64 {
		return nil, errors.New("商户订单号格式错误")
	}
	if req.MerchantNotifyURL != "" {
		if err := safehttp.ValidateURL(ctx, req.MerchantNotifyURL); err != nil {
			return nil, err
		}
	}
	if req.ReturnURL != "" {
		if _, err := safehttp.ParseURL(req.ReturnURL); err != nil {
			return nil, err
		}
	}
	channels, err := s.channelRepo.ListAvailableByPayType(req.PayType)
	if req.ChannelID > 0 {
		var channel *model.Channel
		channel, err = s.channelRepo.GetByID(req.ChannelID)
		if err == nil {
			channels = []model.Channel{*channel}
		}
	}
	if err != nil || len(channels) == 0 {
		return nil, errors.New("暂无可用的支付通道")
	}
	extra, err := json.Marshal(req.Extra)
	if err != nil {
		return nil, err
	}
	var lastError error
	for i := range channels {
		order, err := s.reserveOrder(ctx, req, &channels[i], string(extra))
		if errors.Is(err, errChannelUnavailable) {
			lastError = err
			continue
		}
		if err != nil {
			return nil, err
		}
		// Once reserved, keep this order/channel even if the provider times out.
		// Falling back after a network request could charge the payer twice.
		return s.Checkout(ctx, order.TradeNo, req.PayType)
	}
	return nil, lastError
}

var errChannelUnavailable = errors.New("支付通道当前不可用")

func (s *OrderService) reserveOrder(ctx context.Context, req *CreateOrderRequest, channel *model.Channel, extra string) (*model.Order, error) {
	method := payment.CanonicalMethod(channel.Plugin, req.PayMethod)
	if method == "jsapi" && req.Extra["openid"] == "" {
		return nil, errors.New("JSAPI支付必须提供openid")
	}
	firstQueryAt := FirstQueryAt(time.Now())
	order := model.Order{TradeNo: utils.GenerateTradeNo(), OutTradeNo: req.OutTradeNo, MerchantID: req.MerchantID, ChannelID: channel.ID, PayType: req.PayType, Amount: req.Amount, RealAmount: req.Amount, Name: req.Name, NotifyURL: req.MerchantNotifyURL, ReturnURL: req.ReturnURL, ClientIP: req.ClientIP, PayMethod: method, Extra: extra, NextQueryAt: &firstQueryAt}
	err := database.Get().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(channel, channel.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: 通道已删除", errChannelUnavailable)
			}
			return err
		}
		if channel.Status != 1 {
			return fmt.Errorf("%w: 支付通道已禁用", errChannelUnavailable)
		}
		if err := payment.CheckMethod(channel.Plugin, channel.AppType, method); err != nil {
			return fmt.Errorf("%w: %v", errChannelUnavailable, err)
		}
		merchant, err := s.merchantRepo.GetByIDForUpdate(tx, req.MerchantID)
		if err != nil {
			return err
		}
		if merchant.Status != 1 {
			return errors.New("商户已被禁用")
		}
		var count int64
		if err := tx.Model(&model.Order{}).Where("merchant_id = ? AND out_trade_no = ?", req.MerchantID, req.OutTradeNo).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("商户订单号已存在")
		}
		if channel.DailyLimit.IsPositive() {
			now := time.Now()
			start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			var total decimal.Decimal
			if err := tx.Model(&model.Order{}).Select("COALESCE(SUM(amount),0)").Where("channel_id = ? AND created_at >= ?", channel.ID, start).Scan(&total).Error; err != nil {
				return err
			}
			if total.Add(req.Amount).GreaterThan(channel.DailyLimit) {
				return fmt.Errorf("%w: 通道日限额不足", errChannelUnavailable)
			}
		}
		if channel.Rate.IsNegative() || channel.Rate.GreaterThanOrEqual(decimal.NewFromInt(100)) {
			return errors.New("支付通道费率无效")
		}
		order.Fee = req.Amount.Mul(channel.Rate).Div(decimal.NewFromInt(100)).Round(2)
		// Store the resolved callback rather than trusting a different Host on checkout retries.
		order.ProviderNotifyURL = req.NotifyURL
		if req.PlatformBaseURL != "" {
			order.ProviderNotifyURL = strings.TrimRight(req.PlatformBaseURL, "/") + fmt.Sprintf("/api/pay/notify/%s/%d", channel.Plugin, channel.ID)
		}
		if channel.CallbackURL != "" {
			order.ProviderNotifyURL = channel.CallbackURL
		}
		return tx.Create(&order).Error
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// Checkout always uses the original channel, amount and payer. Cache the result and serialize retries.
func (s *OrderService) Checkout(ctx context.Context, no, payType string) (*CreateOrderResponse, error) {
	var result CreateOrderResponse
	err := database.Get().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		order, err := s.orderRepo.GetByTradeNoForUpdate(tx, no)
		if err != nil {
			return err
		}
		if order.Status != model.OrderStatusUnpaid {
			return errors.New("订单已支付或已退款")
		}
		if payType != "" && payType != order.PayType {
			return errors.New("请使用订单原支付方式")
		}
		result = CreateOrderResponse{TradeNo: order.TradeNo, PayType: order.CheckoutType, PayURL: order.PayURL, PayParams: order.PayParams}
		var channel model.Channel
		if err := tx.Unscoped().First(&channel, order.ChannelID).Error; err != nil {
			return err
		}
		wechatH5 := channel.Plugin == "wechat" && payment.CanonicalMethod(channel.Plugin, order.PayMethod) == "h5"
		cacheValid := order.CheckoutExpiresAt == nil || time.Now().Before(*order.CheckoutExpiresAt)
		// Pre-upgrade H5 caches have no expiry and must be refreshed once.
		if wechatH5 && order.CheckoutExpiresAt == nil {
			cacheValid = false
		}
		if order.CheckoutType != "" && cacheValid {
			return nil
		}
		if channel.Status != 1 || channel.DeletedAt.Valid {
			return errors.New("通道已禁用，无法重新发起支付")
		}
		if err := payment.CheckMethod(channel.Plugin, channel.AppType, order.PayMethod); err != nil {
			return err
		}
		adapter, err := payment.NewAdapter(channel.Plugin, channel.Config)
		if err != nil {
			return fmt.Errorf("支付通道配置错误: %w", err)
		}
		var extra map[string]string
		if order.Extra != "" {
			if err := json.Unmarshal([]byte(order.Extra), &extra); err != nil {
				return err
			}
		}
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		response, err := adapter.CreateOrder(requestCtx, &payment.CreateOrderRequest{TradeNo: no, Amount: order.RealAmount, Subject: order.Name, ClientIP: order.ClientIP, NotifyURL: order.ProviderNotifyURL, ReturnURL: order.ReturnURL, PayMethod: order.PayMethod, Extra: extra})
		if err != nil {
			return err
		}
		if response == nil {
			return errors.New("支付响应为空")
		}
		result.PayType = response.PayType
		result.PayURL = response.PayURL
		result.PayParams = response.PayParams
		var expiresAt *time.Time
		if wechatH5 {
			expiry := time.Now().Add(4*time.Minute + 30*time.Second)
			expiresAt = &expiry
		}
		updates := map[string]any{"checkout_type": response.PayType, "pay_url": response.PayURL, "pay_params": response.PayParams, "checkout_expires_at": expiresAt}
		if order.CheckoutType != "" && !cacheValid {
			updates["query_count"] = 0
			updates["next_query_at"] = FirstQueryAt(time.Now())
		}
		return tx.Model(order).Updates(updates).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// GetByTradeNo 根据订单号获取订单
func (s *OrderService) GetByTradeNo(tradeNo string) (*model.Order, error) {
	return s.orderRepo.GetByTradeNo(tradeNo)
}

// GetByOutTradeNo 根据商户订单号获取订单
func (s *OrderService) GetByOutTradeNo(merchantID int64, outTradeNo string) (*model.Order, error) {
	return s.orderRepo.GetByOutTradeNo(merchantID, outTradeNo)
}

// List 分页查询订单
func (s *OrderService) List(page, pageSize int, merchantID *int64, status *int8) ([]model.Order, int64, error) {
	return s.orderRepo.List(page, pageSize, merchantID, status)
}

// ProcessPayNotify 处理支付回调
func (s *OrderService) ProcessPayNotify(tradeNo, apiTradeNo, buyer string, amount decimal.Decimal, channelID ...int64) error {
	// 回调与主动查单共享此入口。数据库行锁保证跨进程幂等，订单、余额、
	// 流水必须使用同一事务；Transaction 会返回 Begin/Commit 错误并回滚异常。
	return database.Get().Transaction(func(tx *gorm.DB) error {
		order, err := s.orderRepo.GetByTradeNoForUpdate(tx, tradeNo)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("订单不存在")
			}
			return err
		}
		if !order.Amount.Equal(amount) {
			return errors.New("支付金额不匹配")
		}
		if len(channelID) > 0 && order.ChannelID != channelID[0] {
			return errors.New("支付通知通道不匹配")
		}
		switch order.Status {
		case model.OrderStatusPaid, model.OrderStatusRefund:
			return nil // 正常重试无需再次入账，也不能将已退款订单恢复为已支付。
		case model.OrderStatusUnpaid:
		default:
			return errors.New("订单状态不允许入账")
		}

		merchant, err := s.merchantRepo.GetByIDForUpdate(tx, order.MerchantID)
		if err != nil {
			return err
		}
		income := order.Amount.Sub(order.Fee)
		newBalance := merchant.Balance.Add(income)
		if err := s.orderRepo.UpdatePayInfo(tx, tradeNo, apiTradeNo, buyer); err != nil {
			return err
		}
		if err := s.merchantRepo.UpdateBalance(tx, merchant.ID, income); err != nil {
			return err
		}
		return repository.AddBalanceRecord(tx, merchant.ID, model.RecordActionIncome, income, merchant.Balance, newBalance, "order_income", tradeNo)
	})
}

// GetTodayStats 获取今日统计
func (s *OrderService) GetTodayStats(merchantID *int64) (int64, decimal.Decimal, error) {
	return s.orderRepo.GetTodayStats(merchantID)
}

// CreateTestOrder 创建测试订单
func (s *OrderService) CreateTestOrder(channelID int64, amount, method, base string, extra ...map[string]string) (*model.Order, interface{}, error) {
	channel, err := s.channelRepo.GetByID(channelID)
	if err != nil {
		return nil, nil, err
	}
	merchant, err := s.merchantRepo.GetFirst()
	if err != nil {
		return nil, nil, errors.New("请先创建一个商户再测试支付")
	}
	money, err := decimal.NewFromString(amount)
	if err != nil {
		return nil, nil, err
	}
	family := "alipay"
	if channel.Plugin == "wechat" || channel.Plugin == "hf-wxpay" {
		family = "wxpay"
	}
	var params map[string]string
	if len(extra) > 0 {
		params = extra[0]
	}
	response, err := s.Create(context.Background(), &CreateOrderRequest{ChannelID: channelID, MerchantID: merchant.ID, OutTradeNo: "TEST" + utils.GenerateTradeNo(), Amount: money, Name: "测试支付", PayType: family, PayMethod: method, PlatformBaseURL: base, ClientIP: "127.0.0.1", Extra: params})
	if err != nil {
		return nil, nil, err
	}
	order, err := s.GetByTradeNo(response.TradeNo)
	if err != nil {
		return nil, nil, err
	}
	return order, map[string]interface{}{"pay_type": response.PayType, "pay_url": response.PayURL, "pay_params": response.PayParams}, nil
}
