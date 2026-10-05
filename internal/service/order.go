// internal/service/order.go
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/utils"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
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
	// 检查商户订单号是否重复
	existOrder, _ := s.orderRepo.GetByOutTradeNo(req.MerchantID, req.OutTradeNo)
	if existOrder != nil {
		return nil, errors.New("商户订单号已存在")
	}

	// 获取可用通道
	channel, err := s.channelRepo.GetAvailableByPayType(req.PayType)
	if err != nil {
		return nil, errors.New("暂无可用的支付通道")
	}

	// 创建支付适配器
	adapter, err := payment.NewAdapter(channel.Plugin, channel.Config)
	if err != nil {
		log.Printf("create payment adapter failed: channel_id=%d plugin=%s err=%v", channel.ID, channel.Plugin, err)
		return nil, fmt.Errorf("支付通道配置错误: %w", err)
	}

	// 生成订单号
	tradeNo := utils.GenerateTradeNo()

	// 计算手续费
	fee := req.Amount.Mul(channel.Rate).Round(2)
	realAmount := req.Amount

	// 首次主动查单时间
	firstQueryAt := FirstQueryAt(time.Now())

	// 创建订单记录
	order := &model.Order{
		TradeNo:      tradeNo,
		OutTradeNo:   req.OutTradeNo,
		MerchantID:   req.MerchantID,
		ChannelID:    channel.ID,
		PayType:      req.PayType,
		Amount:       req.Amount,
		RealAmount:   realAmount,
		Fee:          fee,
		Name:         req.Name,
		NotifyURL:    req.MerchantNotifyURL,
		ReturnURL:    req.ReturnURL,
		ClientIP:     req.ClientIP,
		Status:       model.OrderStatusUnpaid,
		NotifyStatus: model.NotifyStatusPending,
		NextQueryAt:  &firstQueryAt,
	}

	if err := s.orderRepo.Create(order); err != nil {
		return nil, err
	}

	// 调用支付接口
	payMethod := req.PayMethod
	if payMethod == "" {
		payMethod = "scan" // 默认扫码
	}

	providerNotifyURL := req.NotifyURL
	if req.PlatformBaseURL != "" {
		providerNotifyURL = strings.TrimRight(req.PlatformBaseURL, "/") + "/api/pay/notify/" + channel.Plugin
	}
	// 通道配置了回调URL时才覆盖；未配置(空字符串)则完全走上面的 Host 拼接逻辑，行为不变
	if channel.CallbackURL != "" {
		providerNotifyURL = channel.CallbackURL
	}
	log.Printf("Provider notify url resolved: trade_no=%s channel=%s url=%s", tradeNo, channel.Plugin, providerNotifyURL)

	payReq := &payment.CreateOrderRequest{
		TradeNo:   tradeNo,
		Amount:    realAmount,
		Subject:   req.Name,
		ClientIP:  req.ClientIP,
		NotifyURL: providerNotifyURL,
		ReturnURL: req.ReturnURL,
		PayMethod: payMethod,
		Extra:     req.Extra,
	}

	payResp, err := adapter.CreateOrder(ctx, payReq)
	if err != nil {
		return nil, err
	}

	return &CreateOrderResponse{
		TradeNo:   tradeNo,
		PayType:   payResp.PayType,
		PayURL:    payResp.PayURL,
		PayParams: payResp.PayParams,
	}, nil
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
func (s *OrderService) ProcessPayNotify(tradeNo, apiTradeNo, buyer string, amount decimal.Decimal) error {
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
func (s *OrderService) CreateTestOrder(channelID int64, amount, payType, platformBaseURL string) (*model.Order, interface{}, error) {
	// 获取通道信息
	channel, err := s.channelRepo.GetByID(channelID)
	if err != nil {
		return nil, nil, errors.New("通道不存在")
	}

	// 选择一个真实存在的商户挂载测试订单，避免外键约束失败
	merchant, err := s.merchantRepo.GetFirst()
	if err != nil {
		return nil, nil, errors.New("请先创建一个商户再进行测试支付")
	}

	// 解析金额
	amountDecimal, err := decimal.NewFromString(amount)
	if err != nil {
		return nil, nil, errors.New("金额格式错误")
	}

	// 创建测试订单（挂载到现有商户）
	tradeNo := utils.GenerateTradeNo()
	firstQueryAt := FirstQueryAt(time.Now())
	order := &model.Order{
		TradeNo:      tradeNo,
		OutTradeNo:   "TEST" + tradeNo,
		MerchantID:   merchant.ID,
		ChannelID:    channelID,
		PayType:      payType,
		Amount:       amountDecimal,
		RealAmount:   amountDecimal,
		Fee:          decimal.Zero,
		Name:         "测试支付",
		NotifyURL:    "",
		ReturnURL:    "",
		Status:       model.OrderStatusUnpaid,
		NotifyStatus: model.NotifyStatusPending,
		NextQueryAt:  &firstQueryAt,
	}

	if err := s.orderRepo.Create(order); err != nil {
		return nil, nil, err
	}

	// 创建支付适配器
	adapter, err := payment.NewAdapter(channel.Plugin, channel.Config)
	if err != nil {
		log.Printf("create test payment adapter failed: channel_id=%d plugin=%s err=%v", channel.ID, channel.Plugin, err)
		return nil, nil, fmt.Errorf("支付通道配置错误: %w", err)
	}

	// 调用支付接口
	providerNotifyURL := strings.TrimRight(platformBaseURL, "/") + "/api/pay/notify/" + channel.Plugin
	if channel.CallbackURL != "" {
		providerNotifyURL = channel.CallbackURL
	}
	log.Printf("Provider notify url resolved: trade_no=%s channel=%s url=%s", tradeNo, channel.Plugin, providerNotifyURL)
	payReq := &payment.CreateOrderRequest{
		TradeNo:   tradeNo,
		Amount:    amountDecimal,
		Subject:   "测试支付",
		ClientIP:  "127.0.0.1",
		NotifyURL: providerNotifyURL,
		ReturnURL: "",
		PayMethod: payType,
		Extra:     nil,
	}

	ctx := context.Background()
	payResp, err := adapter.CreateOrder(ctx, payReq)
	if err != nil {
		return nil, nil, err
	}

	// 返回订单和支付数据
	payData := map[string]interface{}{
		"pay_type":   payResp.PayType,
		"pay_url":    payResp.PayURL,
		"pay_params": payResp.PayParams,
	}

	return order, payData, nil
}
