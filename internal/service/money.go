package service

import (
	"errors"
	"github.com/shopspring/decimal"
)

func validateMoney(amount decimal.Decimal) error {
	if !amount.IsPositive() || !amount.Equal(amount.Round(2)) || amount.GreaterThan(decimal.RequireFromString("9999999999.99")) {
		return errors.New("金额必须大于零、最多两位小数且不超过9999999999.99")
	}
	return nil
}
