// internal/database/migrate.go
package database

import (
	"fmt"
	"log"

	"github.com/example/epay-go/internal/model"
)

// Migrate 自动迁移数据库表
func Migrate() error {
	log.Println("Running database migrations...")

	// Do not delete historical payment records to make the new unique index fit.
	if DB.Migrator().HasTable(&model.Order{}) {
		var duplicates int64
		if err := DB.Raw(`SELECT COUNT(*) FROM (SELECT merchant_id,out_trade_no FROM orders GROUP BY merchant_id,out_trade_no HAVING COUNT(*)>1) AS duplicate_orders`).Scan(&duplicates).Error; err != nil {
			return err
		}
		if duplicates > 0 {
			return fmt.Errorf("发现 %d 组重复商户订单号；请按 DEPLOYMENT.md 核对历史订单后再迁移", duplicates)
		}
	}
	err := DB.AutoMigrate(
		&model.Admin{},
		&model.Merchant{},
		&model.Channel{},
		&model.Order{},
		&model.Settlement{},
		&model.BalanceRecord{},
		&model.Config{},
		&model.Refund{},
	)

	if err != nil {
		return err
	}

	log.Println("Database migrations completed")
	return nil
}
