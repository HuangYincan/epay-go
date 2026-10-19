// Operator-only read-only CLI. It never invokes migrations or a payment query API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/example/epay-go/internal/observerstore"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func run() int {
	var in observerstore.PilotPreflightInput
	flag.StringVar(&in.Environment, "environment", "", "fixed notification environment")
	flag.StringVar(&in.ReceiverID, "receiver", "", "previously specified receiver")
	flag.StringVar(&in.ParserVersion, "parser", "", "reviewed parser version")
	flag.Int64Var(&in.OrderID, "order-id", 0, "existing original order id")
	flag.Int64Var(&in.ActorID, "actor-id", 0, "existing administrator id")
	flag.Int64Var(&in.ExpiresAtMs, "expires-at-ms", 0, "explicit deadline; at most ten minutes")
	flag.Parse()
	if flag.NArg() != 0 || os.Getenv("EPAY_PREFLIGHT_DSN") == "" {
		fmt.Fprintln(os.Stderr, "preflight_input_required")
		return 2
	}
	db, err := gorm.Open(postgres.Open(os.Getenv("EPAY_PREFLIGHT_DSN")), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "preflight_database_unavailable")
		return 2
	}
	pool, err := db.DB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "preflight_database_unavailable")
		return 2
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in.NowMs = time.Now().UnixMilli()
	report, err := observerstore.InspectNotificationPilot(ctx, db, in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "preflight_failed")
		return 2
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return 2
	}
	if !report.Ready {
		return 1
	}
	return 0
}

func main() { os.Exit(run()) }
