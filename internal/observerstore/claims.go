package observerstore

import (
	"errors"
	"time"

	"github.com/example/epay-go/internal/model"
	"gorm.io/gorm"
)

var ErrSyntheticDisabled = errors.New("观察器合成支付范围未启用")
var ErrAmountUnavailable = errors.New("此金额当前不可使用")
var ErrPaymentWindow = errors.New("观察器支付窗口已过期或时间无效")
var ErrObserverSettlement = errors.New("观察器订单必须通过事件证据结算")

const SyntheticPolicy = "synthetic-exclusive-v1"
const PaymentWindow = 10 * time.Minute

// OrderScope holds receiver locks in the caller's order creation transaction.
// No committed payment window may exist without its immutable amount claim.
type OrderScope struct {
	Environment string
	ReceiverID  string
	Provider    string
}

func ChannelHasObserverAlias(tx *gorm.DB, channelID int64) (bool, error) {
	var n int64
	err := tx.Table("observer_channel_aliases").Where("channel_id = ?", channelID).Count(&n).Error
	return n != 0, err
}

func LockOrderScope(tx *gorm.DB, channelID, merchantID int64, provider string) (*OrderScope, error) {
	var aliases []OrderScope
	if err := tx.Raw(`SELECT environment, receiver_id, provider FROM observer_channel_aliases WHERE channel_id = ?`, channelID).Scan(&aliases).Error; err != nil {
		return nil, err
	}
	if len(aliases) == 0 {
		return nil, nil
	}
	var scope *OrderScope
	for i := range aliases {
		if aliases[i].Provider == provider {
			scope = &aliases[i]
		}
	}
	if scope == nil || scope.Environment == "" || scope.Environment != SyntheticEnvironment() {
		return nil, ErrSyntheticDisabled
	}
	var environment struct {
		Enabled     bool
		SourceClass string
	}
	if err := tx.Raw(`SELECT enabled, source_class FROM observer_environments WHERE environment = ? FOR SHARE`, scope.Environment).Scan(&environment).Error; err != nil {
		return nil, err
	}
	if !environment.Enabled || environment.SourceClass != "synthetic" {
		return nil, ErrSyntheticDisabled
	}
	var receiver struct{ Enabled bool }
	if err := tx.Raw(`SELECT enabled FROM observer_receivers WHERE environment = ? AND receiver_id = ? AND provider = ? FOR UPDATE`, scope.Environment, scope.ReceiverID, scope.Provider).Scan(&receiver).Error; err != nil {
		return nil, err
	}
	if !receiver.Enabled {
		return nil, ErrSyntheticDisabled
	}
	var merchant struct{ Enabled bool }
	if err := tx.Raw(`SELECT enabled FROM observer_synthetic_merchants WHERE environment = ? AND merchant_id = ? FOR SHARE`, scope.Environment, merchantID).Scan(&merchant).Error; err != nil {
		return nil, err
	}
	if !merchant.Enabled {
		return nil, ErrSyntheticDisabled
	}
	return scope, nil
}

// Claim is called after the new order INSERT, inside the same transaction.
func (scope *OrderScope) Claim(tx *gorm.DB, order *model.Order) error {
	if scope == nil {
		return nil
	}
	// All history counts, including paid/refunded/deleted orders and other aliases.
	var history int64
	if err := tx.Raw(`SELECT count(*) FROM orders o JOIN observer_channel_aliases a ON a.channel_id = o.channel_id AND a.provider = o.pay_type WHERE a.environment = ? AND a.receiver_id = ? AND a.provider = ? AND o.amount = ? AND o.id <> ?`, scope.Environment, scope.ReceiverID, scope.Provider, order.Amount, order.ID).Scan(&history).Error; err != nil {
		return err
	}
	if history != 0 {
		return ErrAmountUnavailable
	}
	if order.ObserverPayExpiresAt == nil {
		return ErrPaymentWindow
	}
	inserted := tx.Exec(`INSERT INTO observer_amount_claims(environment,receiver_id,provider,amount_minor,order_id,channel_id,merchant_id,created_at_ms,observer_pay_expires_at_ms,policy_version) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, scope.Environment, scope.ReceiverID, scope.Provider, order.Amount.Shift(2).IntPart(), order.ID, order.ChannelID, order.MerchantID, order.CreatedAt.UnixMilli(), order.ObserverPayExpiresAt.UnixMilli(), SyntheticPolicy)
	if inserted.Error != nil {
		return inserted.Error
	}
	if inserted.RowsAffected != 1 {
		return ErrAmountUnavailable
	}
	return nil
}

// CheckOrderWindow performs read-only scope validation after the order lock.
// It never acquires receiver locks in reverse order or extends a cached QR.
func CheckOrderWindow(tx *gorm.DB, order *model.Order, now time.Time) error {
	mapped, err := ChannelHasObserverAlias(tx, order.ChannelID)
	if err != nil {
		return err
	}
	if order.ObserverPayExpiresAt == nil && !mapped {
		return nil
	}
	var claim struct {
		Environment            string
		CreatedAtMs            int64
		ObserverPayExpiresAtMs int64
		Enabled                bool
	}
	result := tx.Raw(`SELECT c.environment, c.created_at_ms, c.observer_pay_expires_at_ms,
      (e.enabled AND e.source_class='synthetic' AND r.enabled AND m.enabled AND merchant.status=1 AND merchant.deleted_at IS NULL) AS enabled
      FROM observer_amount_claims c JOIN observer_environments e USING(environment)
      JOIN observer_receivers r ON r.environment=c.environment AND r.receiver_id=c.receiver_id AND r.provider=c.provider
      JOIN observer_synthetic_merchants m ON m.environment=c.environment AND m.merchant_id=c.merchant_id
      JOIN merchants merchant ON merchant.id=c.merchant_id
      WHERE c.order_id = ?`, order.ID).Scan(&claim)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || !claim.Enabled || claim.Environment != SyntheticEnvironment() {
		return ErrSyntheticDisabled
	}
	if order.ObserverPayExpiresAt == nil || now.UnixMilli() < claim.CreatedAtMs || now.UnixMilli() >= claim.ObserverPayExpiresAtMs {
		return ErrPaymentWindow
	}
	return nil
}

func RejectLegacySettlement(tx *gorm.DB, order *model.Order) error {
	mapped, err := ChannelHasObserverAlias(tx, order.ChannelID)
	if err != nil {
		return err
	}
	if mapped || order.ObserverPayExpiresAt != nil {
		return ErrObserverSettlement
	}
	return nil
}
