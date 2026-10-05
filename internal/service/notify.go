// internal/service/notify.go
package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/repository"
	"github.com/example/epay-go/pkg/safehttp"
)

type NotifyService struct {
	orderRepo    *repository.OrderRepository
	merchantRepo *repository.MerchantRepository
	httpClient   *http.Client
}

func NewNotifyService() *NotifyService {
	return &NotifyService{
		orderRepo:    repository.NewOrderRepository(),
		merchantRepo: repository.NewMerchantRepository(),
		httpClient:   safehttp.NewClient(),
	}
}

// NotifyRetryIntervals 通知重试间隔
var NotifyRetryIntervals = []time.Duration{
	0,                // 立即
	1 * time.Minute,  // 1分钟后
	3 * time.Minute,  // 3分钟后
	20 * time.Minute, // 20分钟后
	1 * time.Hour,    // 1小时后
	2 * time.Hour,    // 2小时后
}

// SendNotify 发送回调通知
func (s *NotifyService) SendNotify(order *model.Order) error {
	if strings.TrimSpace(order.NotifyURL) == "" {
		log.Printf("Skip merchant notify: trade_no=%s reason=empty_notify_url", order.TradeNo)
		return s.orderRepo.UpdateNotifyStatus(order.TradeNo, model.NotifyStatusSuccess, nil)
	}

	// 获取商户信息
	merchant, err := s.merchantRepo.GetByID(order.MerchantID)
	if err != nil {
		return err
	}

	// 构建通知参数
	params := s.buildNotifyParams(order, merchant)
	log.Printf("Send merchant notify: trade_no=%s notify_url=%s notify_count=%d", order.TradeNo, order.NotifyURL, order.NotifyCount)

	// 发送请求
	success := s.doNotify(order.NotifyURL, params)

	// 更新通知状态
	if success {
		log.Printf("Merchant notify success: trade_no=%s notify_url=%s", order.TradeNo, order.NotifyURL)
		return s.orderRepo.UpdateNotifyStatus(order.TradeNo, model.NotifyStatusSuccess, nil)
	}

	// 通知失败，计算下次重试时间
	nextCount := order.NotifyCount + 1
	if nextCount >= len(NotifyRetryIntervals) {
		// 重试次数用尽
		return s.orderRepo.UpdateNotifyStatus(order.TradeNo, model.NotifyStatusFailed, nil)
	}

	nextTime := time.Now().Add(NotifyRetryIntervals[nextCount])
	log.Printf("Merchant notify scheduled retry: trade_no=%s notify_url=%s next_notify_at=%s", order.TradeNo, order.NotifyURL, nextTime.Format(time.RFC3339))
	return s.orderRepo.UpdateNotifyStatus(order.TradeNo, model.NotifyStatusSending, &nextTime)
}

// buildNotifyParams 构建通知参数
func (s *NotifyService) buildNotifyParams(order *model.Order, merchant *model.Merchant) url.Values {
	params := url.Values{}
	params.Set("pid", fmt.Sprintf("%d", order.MerchantID))
	params.Set("trade_no", order.TradeNo)
	params.Set("out_trade_no", order.OutTradeNo)
	params.Set("type", order.PayType)
	params.Set("name", order.Name)
	params.Set("money", order.Amount.String())
	params.Set("trade_status", "TRADE_SUCCESS")

	if order.ApiTradeNo != "" {
		params.Set("api_trade_no", order.ApiTradeNo)
	}
	if order.Buyer != "" {
		params.Set("buyer", order.Buyer)
	}

	// 生成签名
	sign := s.generateSign(params, merchant.ApiKey)
	params.Set("sign", sign)
	params.Set("sign_type", "MD5")

	return params
}

// generateSign 生成MD5签名（与原epay兼容）
func (s *NotifyService) generateSign(params url.Values, key string) string {
	// 按key排序
	var keys []string
	for k := range params {
		if k != "sign" && k != "sign_type" && params.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	// 拼接字符串
	var buf strings.Builder
	for i, k := range keys {
		if i > 0 {
			buf.WriteString("&")
		}
		buf.WriteString(k)
		buf.WriteString("=")
		buf.WriteString(params.Get(k))
	}
	buf.WriteString(key)

	// MD5
	hash := md5.Sum([]byte(buf.String()))
	return hex.EncodeToString(hash[:])
}

// doNotify 执行通知请求
func (s *NotifyService) doNotify(notifyURL string, params url.Values) bool {
	if err := safehttp.ValidateURL(context.Background(), notifyURL); err != nil {
		return false
	}
	// 构建完整URL
	fullURL := notifyURL
	if strings.Contains(notifyURL, "?") {
		fullURL += "&" + params.Encode()
	} else {
		fullURL += "?" + params.Encode()
	}

	resp, err := s.httpClient.Get(fullURL)
	if err != nil {
		log.Printf("Notify request failed: %v", err)
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return false
	}
	response := strings.ToLower(strings.TrimSpace(string(body)))
	log.Printf("Merchant notify response: url=%s status=%d body=%s", notifyURL, resp.StatusCode, strings.TrimSpace(string(body)))

	// 检查响应是否为 success
	return resp.StatusCode >= 200 && resp.StatusCode < 300 && response == "success"
}

// StartNotifyWorker 启动通知工作协程
func (s *NotifyService) StartNotifyWorker(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Notify worker stopped")
			return
		case <-ticker.C:
			s.processNotifyQueue()
		}
	}
}

// processNotifyQueue 处理通知队列
func (s *NotifyService) processNotifyQueue() {
	orders, err := s.orderRepo.GetPendingNotifyOrders(50)
	if err != nil {
		log.Printf("Get pending notify orders failed: %v", err)
		return
	}

	for _, order := range orders {
		if err := s.SendNotify(&order); err != nil {
			log.Printf("Send notify failed for %s: %v", order.TradeNo, err)
		}
	}
}
