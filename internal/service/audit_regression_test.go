package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/internal/repository"
	"github.com/shopspring/decimal"
)

type notifyTestTransport func(*http.Request) (*http.Response, error)

func (f notifyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func notificationFixture(t *testing.T) (*NotifyService, model.Order) {
	t.Helper()
	db := paymentTestDB(t)
	_, orders := paymentFixture(t, db, 1)
	o := orders[0]
	if err := db.Model(&o).Updates(map[string]any{"status": model.OrderStatusPaid, "notify_url": "https://8.8.8.8/callback"}).Error; err != nil {
		t.Fatal(err)
	}
	return NewNotifyService(), o
}

func TestNotificationConcurrentDelivery(t *testing.T) {
	svc, o := notificationFixture(t)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	svc.httpClient = &http.Client{Transport: notifyTestTransport(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("success"))}, nil
	})}
	done := make(chan error, 1)
	go func() { done <- svc.SendNotify(&o) }()
	<-entered
	concurrentPaymentCalls(t, 8, func(int) error { return svc.SendNotify(&o) })
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := svc.SendNotify(&o); err != nil {
		t.Fatal(err)
	}
	got, err := NewOrderService().GetByTradeNo(o.TradeNo)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || got.NotifyCount != 1 || got.NotifyStatus != model.NotifyStatusSuccess || got.NotifyLeaseUntil != nil || got.NextNotifyAt != nil {
		t.Fatalf("duplicate or unfinished notification: calls=%d order=%+v", calls.Load(), got)
	}
}

func TestNotificationLeaseRecoveryRejectsLateResult(t *testing.T) {
	_, o := notificationFixture(t)
	repo := repository.NewOrderRepository()
	now := time.Now()
	old, err := repo.ClaimNotify(o.TradeNo, "old-owner", now.Add(-5*time.Minute), now.Add(-3*time.Minute), false)
	if err != nil || old == nil {
		t.Fatalf("first claim: %v", err)
	}
	current, err := repo.ClaimNotify(o.TradeNo, "new-owner", now, now.Add(2*time.Minute), false)
	if err != nil || current == nil {
		t.Fatalf("reclaim expired lease: %v", err)
	}
	if err := repo.FinishNotify(o.TradeNo, "new-owner", model.NotifyStatusSuccess, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishNotify(o.TradeNo, "old-owner", model.NotifyStatusFailed, nil); err != nil {
		t.Fatal(err)
	}
	got, err := NewOrderService().GetByTradeNo(o.TradeNo)
	if err != nil {
		t.Fatal(err)
	}
	if got.NotifyStatus != model.NotifyStatusSuccess || got.NotifyCount != 2 {
		t.Fatalf("late result overwrote success: %+v", got)
	}
}

func TestNotificationUsesFreshRetryCount(t *testing.T) {
	svc, snapshot := notificationFixture(t)
	snapshot.NotifyCount = 5 // A stale callback snapshot must not exhaust the DB retry budget.
	var calls atomic.Int32
	svc.httpClient = &http.Client{Transport: notifyTestTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("fail"))}, nil
	})}
	if err := svc.SendNotify(&snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := NewOrderService().GetByTradeNo(snapshot.TradeNo)
	if err != nil {
		t.Fatal(err)
	}
	if got.NotifyCount != 1 || got.NotifyStatus != model.NotifyStatusSending || got.NextNotifyAt == nil || !got.NextNotifyAt.After(time.Now()) || got.NotifyLeaseUntil != nil {
		t.Fatalf("retry scheduling used stale state: %+v", got)
	}
	if err := svc.SendNotify(&snapshot); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("replayed callback bypassed retry delay")
	}
}

func TestAdminNotificationResend(t *testing.T) {
	svc, o := notificationFixture(t)
	var calls atomic.Int32
	svc.httpClient = &http.Client{Transport: notifyTestTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("success"))}, nil
	})}
	if err := svc.SendNotify(&o); err != nil {
		t.Fatal(err)
	}
	if err := svc.SendNotify(&o); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("automatic callback redelivered a successful notification")
	}
	if err := svc.ResendNotify(&o); err != nil {
		t.Fatal(err)
	}
	got, err := NewOrderService().GetByTradeNo(o.TradeNo)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || got.NotifyStatus != model.NotifyStatusSuccess || got.NotifyCount != 1 {
		t.Fatalf("explicit resend lost: %+v, calls=%d", got, calls.Load())
	}
}

func TestDisabledChannelCreation(t *testing.T) {
	db := paymentTestDB(t)
	channel, err := NewChannelService().Create(&CreateChannelRequest{Name: "disabled", Plugin: "wechat", AppType: "native", Status: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.First(channel, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.Status != 0 {
		t.Fatal("disabled channel was enabled")
	}
	channels, err := repository.NewChannelRepository().ListAvailableByPayType("wxpay")
	if err != nil || len(channels) != 0 {
		t.Fatalf("disabled channel routed: %+v / %v", channels, err)
	}
}

func TestOrderRoutingSkipsUnavailableCandidates(t *testing.T) {
	for _, reason := range []string{"method", "daily_limit"} {
		t.Run(reason, func(t *testing.T) {
			db := paymentTestDB(t)
			m, _ := paymentFixture(t, db, 0)
			a := &ledgerAdapter{}
			payment.Register("routing-test", func(json.RawMessage) (payment.PaymentAdapter, error) { return a, nil })
			first := model.Channel{Name: "first", Plugin: "routing-test", PayTypes: "wxpay", AppType: "native", Status: 1, Sort: 1}
			second := model.Channel{Name: "second", Plugin: "routing-test", PayTypes: "wxpay", AppType: "jsapi,native", Status: 1, Sort: 2, Rate: decimal.RequireFromString("0.6")}
			method := "jsapi"
			if reason == "daily_limit" {
				method = "native"
				first.DailyLimit = decimal.NewFromInt(1)
			}
			for _, c := range []*model.Channel{&first, &second} {
				if err := db.Create(c).Error; err != nil {
					t.Fatal(err)
				}
			}
			r := &CreateOrderRequest{MerchantID: m.ID, OutTradeNo: "routed", Amount: decimal.NewFromInt(2), Name: "test", PayType: "wxpay", PayMethod: method, Extra: map[string]string{"openid": "fixture-payer"}}
			result, err := NewOrderService().Create(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			o, err := NewOrderService().GetByTradeNo(result.TradeNo)
			if err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Model(&model.Order{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if o.ChannelID != second.ID || count != 1 || a.createCalls.Load() != 1 {
				t.Fatalf("wrong routing: %+v, orders=%d calls=%d", o, count, a.createCalls.Load())
			}
			// Explicit channel requests must never silently switch channels.
			r.OutTradeNo, r.ChannelID = "explicit", first.ID
			if _, err := NewOrderService().Create(context.Background(), r); err == nil {
				t.Fatal("explicit channel silently changed")
			}
			if a.createCalls.Load() != 1 {
				t.Fatal("unavailable explicit channel called provider")
			}
		})
	}
}

type timeoutOrderAdapter struct{ ledgerAdapter }

func (a *timeoutOrderAdapter) CreateOrder(context.Context, *payment.CreateOrderRequest) (*payment.CreateOrderResponse, error) {
	a.createCalls.Add(1)
	return nil, context.DeadlineExceeded
}

func TestOrderRoutingKeepsProviderAfterTimeout(t *testing.T) {
	db := paymentTestDB(t)
	m, _ := paymentFixture(t, db, 0)
	firstAdapter, secondAdapter := &timeoutOrderAdapter{}, &ledgerAdapter{}
	payment.Register("timeout-first", func(json.RawMessage) (payment.PaymentAdapter, error) { return firstAdapter, nil })
	payment.Register("timeout-second", func(json.RawMessage) (payment.PaymentAdapter, error) { return secondAdapter, nil })
	first := model.Channel{Plugin: "timeout-first", PayTypes: "alipay", AppType: "native", Status: 1, Sort: 1}
	second := model.Channel{Plugin: "timeout-second", PayTypes: "alipay", AppType: "native", Status: 1, Sort: 2}
	for _, channel := range []*model.Channel{&first, &second} {
		if err := db.Create(channel).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewOrderService().Create(context.Background(), orderRequest(m.ID, "timeout-order", 1)); err == nil {
		t.Fatal("provider timeout hidden")
	}
	var orders []model.Order
	if err := db.Find(&orders).Error; err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].ChannelID != first.ID || firstAdapter.createCalls.Load() != 1 || secondAdapter.createCalls.Load() != 0 {
		t.Fatalf("uncertain payment switched providers: %+v", orders)
	}
}

type h5FixtureAdapter struct {
	ledgerAdapter
	last atomic.Pointer[payment.CreateOrderRequest]
}

func (a *h5FixtureAdapter) CreateOrder(_ context.Context, req *payment.CreateOrderRequest) (*payment.CreateOrderResponse, error) {
	a.last.Store(req)
	count := a.createCalls.Add(1)
	return &payment.CreateOrderResponse{PayType: "redirect", PayURL: fmt.Sprintf("https://wx.tenpay.com/pay?token=%d", count)}, nil
}

func TestWechatH5CheckoutRefresh(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_%t", legacy), func(t *testing.T) {
			db := paymentTestDB(t)
			m, orders := paymentFixture(t, db, 1)
			o := orders[0]
			if err := db.Model(&model.Channel{}).Where("id = ?", o.ChannelID).Updates(map[string]any{"status": 1, "app_type": "h5"}).Error; err != nil {
				t.Fatal(err)
			}
			a := &h5FixtureAdapter{}
			payment.Register("wechat", func(json.RawMessage) (payment.PaymentAdapter, error) { return a, nil })
			t.Cleanup(func() { payment.Register("wechat", payment.NewWechatAdapter) })
			expired := time.Now().Add(-10 * time.Minute)
			var expiry *time.Time
			if !legacy {
				expiry = &expired
			}
			if err := db.Model(&o).Updates(map[string]any{"checkout_type": "redirect", "pay_method": "h5", "pay_url": "https://wx.tenpay.com/expired", "checkout_expires_at": expiry, "query_count": 5, "next_query_at": nil}).Error; err != nil {
				t.Fatal(err)
			}
			svc := NewOrderService()
			concurrentPaymentCalls(t, 4, func(int) error {
				result, err := svc.Checkout(context.Background(), o.TradeNo, "wxpay")
				if err == nil && result.PayURL != "https://wx.tenpay.com/pay?token=1" {
					return fmt.Errorf("wrong refreshed URL: %s", result.PayURL)
				}
				return err
			})
			got, err := svc.GetByTradeNo(o.TradeNo)
			if err != nil {
				t.Fatal(err)
			}
			request := a.last.Load()
			if a.createCalls.Load() != 1 || request == nil || request.TradeNo != o.TradeNo || !request.Amount.Equal(o.RealAmount) || got.CheckoutExpiresAt == nil || !got.CheckoutExpiresAt.After(time.Now()) || got.QueryCount != 0 || got.NextQueryAt == nil || got.ChannelID != o.ChannelID || got.MerchantID != m.ID {
				t.Fatalf("refresh did not preserve order / cache: %+v / %+v", got, request)
			}
			// A worker that read the old final attempt cannot terminate the new schedule.
			if err := repository.NewOrderRepository().UpdateQueryStatus(o.TradeNo, nil, 5); err != nil {
				t.Fatal(err)
			}
			got, err = svc.GetByTradeNo(o.TradeNo)
			if err != nil {
				t.Fatal(err)
			}
			if got.NextQueryAt == nil || got.QueryCount != 0 {
				t.Fatal("stale query result cleared refreshed schedule")
			}
		})
	}
}

func TestArchivedChannelRefundRecovery(t *testing.T) {
	db, m, o, r, a := refundFixture(t, "processing")
	svc := NewRefundService()
	if err := svc.ProcessRefund(r.RefundNo, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := NewChannelService().Delete(o.ChannelID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NewChannelRepository().GetByID(o.ChannelID); err == nil {
		t.Fatal("archived channel visible to new routing")
	}
	if err := db.Model(r).Update("next_query_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(context.Background(), r.RefundNo); err != nil {
		t.Fatal(err)
	}
	if err := db.First(r, r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&m, m.ID).Error; err != nil {
		t.Fatal(err)
	}
	if r.Status != model.RefundStatusSuccess || r.FundsReserved || !m.FrozenBalance.IsZero() || a.calls.Load() != 1 {
		t.Fatalf("archived refund could not finish: %+v / %+v", r, m)
	}
	// Refunds requested after archiving must also retain access to the original provider.
	next, err := svc.CreateRefund(m.ID, &CreateRefundRequest{TradeNo: o.TradeNo, Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	a.refundStatus = "success"
	if err := svc.ProcessRefund(next.RefundNo, true, ""); err != nil {
		t.Fatal(err)
	}
	if a.calls.Load() != 2 {
		t.Fatal("new refund did not use archived original provider")
	}
}
