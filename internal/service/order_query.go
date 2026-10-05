// internal/service/order_query.go
package service

import (
	"context"
	"log"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/payment"
	"github.com/example/epay-go/internal/repository"
)

// QueryIntervals 主动查单相邻两次查询的间隔（累加）：创建后20s第1次，之后依次再等30s/60s/120s/300s
var QueryIntervals = []time.Duration{
	20 * time.Second,
	30 * time.Second,
	60 * time.Second,
	120 * time.Second,
	300 * time.Second,
}

// FirstQueryAt 计算订单创建时应写入的首次主动查单时间
func FirstQueryAt(from time.Time) time.Time {
	return from.Add(QueryIntervals[0])
}

// OrderQueryService 订单主动查单补偿服务
type OrderQueryService struct {
	orderRepo   *repository.OrderRepository
	channelRepo *repository.ChannelRepository
	orderSvc    *OrderService
	notifySvc   *NotifyService
}

func NewOrderQueryService() *OrderQueryService {
	return &OrderQueryService{
		orderRepo:   repository.NewOrderRepository(),
		channelRepo: repository.NewChannelRepository(),
		orderSvc:    NewOrderService(),
		notifySvc:   NewNotifyService(),
	}
}

// StartQueryWorker 启动主动查单工作协程
func (s *OrderQueryService) StartQueryWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Order query worker stopped")
			return
		case <-ticker.C:
			s.processQueryQueue()
		}
	}
}

// processQueryQueue 处理待主动查询的订单队列
func (s *OrderQueryService) processQueryQueue() {
	orders, err := s.orderRepo.GetPendingQueryOrders(50)
	if err != nil {
		log.Printf("Get pending query orders failed: %v", err)
		return
	}

	for _, order := range orders {
		s.queryAndProcess(&order)
	}
}

// queryAndProcess 主动查询单个订单的上游状态并处理
func (s *OrderQueryService) queryAndProcess(order *model.Order) {
	channel, err := s.channelRepo.GetHistoricalByID(order.ChannelID)
	if err != nil {
		log.Printf("Active query: channel not found trade_no=%s: %v", order.TradeNo, err)
		s.scheduleNext(order)
		return
	}

	adapter, err := payment.NewAdapter(channel.Plugin, channel.Config)
	if err != nil {
		log.Printf("Active query: create adapter failed trade_no=%s: %v", order.TradeNo, err)
		s.scheduleNext(order)
		return
	}

	resp, err := adapter.QueryOrder(context.Background(), order.TradeNo)
	if err != nil {
		log.Printf("Active query failed trade_no=%s: %v", order.TradeNo, err)
		s.scheduleNext(order)
		return
	}

	log.Printf("Active query result: trade_no=%s status=%s", order.TradeNo, resp.Status)

	switch resp.Status {
	case "paid":
		if err := s.orderSvc.ProcessPayNotify(order.TradeNo, resp.ApiTradeNo, "", resp.Amount); err != nil {
			log.Printf("Active query: process pay notify failed trade_no=%s: %v", order.TradeNo, err)
			s.scheduleNext(order)
			return
		}
		if err := s.orderRepo.UpdateQueryStatus(order.TradeNo, nil, order.QueryCount); err != nil {
			log.Printf("Active query: update query status failed trade_no=%s: %v", order.TradeNo, err)
		}
		if paidOrder, err := s.orderSvc.GetByTradeNo(order.TradeNo); err == nil && paidOrder.Status == model.OrderStatusPaid {
			go s.notifySvc.SendNotify(paidOrder)
		}
	case "closed":
		if err := s.orderRepo.UpdateQueryStatus(order.TradeNo, nil, order.QueryCount); err != nil {
			log.Printf("Active query: update query status failed trade_no=%s: %v", order.TradeNo, err)
		}
	default:
		s.scheduleNext(order)
	}
}

// scheduleNext 按累加间隔安排下一次查询，超出重试次数则终止调度
func (s *OrderQueryService) scheduleNext(order *model.Order) {
	nextIdx := order.QueryCount + 1
	var nextAt *time.Time
	if nextIdx < len(QueryIntervals) {
		t := time.Now().Add(QueryIntervals[nextIdx])
		nextAt = &t
	}
	if err := s.orderRepo.UpdateQueryStatus(order.TradeNo, nextAt, order.QueryCount); err != nil {
		log.Printf("Update query status failed trade_no=%s: %v", order.TradeNo, err)
	}
}
