// Local operator only. Default read-only. No HTTP server, migration, credential decryption or payment API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/example/epay-go/internal/observerstore"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func run() int {
	input := flag.String("input", "", "private reviewed scope JSON")
	output := flag.String("output", "", "new private Android handoff file (apply only)")
	apply := flag.Bool("apply", false, "explicitly register or recover the same reviewed pilot")
	flag.Parse()
	if flag.NArg() != 0 || *input == "" || os.Getenv("EPAY_OPERATOR_DSN") == "" || *apply != (*output != "") {
		fmt.Fprintln(os.Stderr, "operator_input_required")
		return 2
	}
	file, e := os.Open(*input)
	if e != nil {
		fmt.Fprintln(os.Stderr, "operator_input_invalid")
		return 2
	}
	bytes, e := io.ReadAll(io.LimitReader(file, 4097))
	file.Close()
	if e != nil {
		fmt.Fprintln(os.Stderr, "operator_input_invalid")
		return 2
	}
	in, e := observerstore.DecodePilotOperatorInput(bytes)
	if e != nil || *apply && !in.ReviewComplete() {
		fmt.Fprintln(os.Stderr, "operator_review_invalid")
		return 2
	}
	db, e := gorm.Open(postgres.Open(os.Getenv("EPAY_OPERATOR_DSN")), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		fmt.Fprintln(os.Stderr, "operator_database_unavailable")
		return 2
	}
	pool, e := db.DB()
	if e != nil {
		fmt.Fprintln(os.Stderr, "operator_database_unavailable")
		return 2
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UnixMilli()
	if !*apply {
		report, e := observerstore.InspectPilotOperator(ctx, db, in, now)
		if e != nil {
			fmt.Fprintln(os.Stderr, "operator_preflight_failed")
			return 2
		}
		if e = json.NewEncoder(os.Stdout).Encode(struct {
			Preflight   observerstore.PilotPreflight `json:"preflight"`
			ReviewReady bool                         `json:"review_ready"`
			Applied     bool                         `json:"applied"`
		}{report, in.ReviewComplete(), false}); e != nil {
			return 2
		}
		if !report.Ready || !in.ReviewComplete() {
			return 1
		}
		return 0
	}
	// Reserve before any registration; never overwrite an old artifact or follow a final symlink.
	target, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		fmt.Fprintln(os.Stderr, "operator_output_unavailable")
		return 2
	}
	written := false
	defer func() {
		target.Close()
		if !written {
			os.Remove(*output)
		}
	}()
	handoff, recovered, e := observerstore.PrepareReviewedNotificationPilot(ctx, db, in, now)
	if e != nil {
		fmt.Fprintln(os.Stderr, "operator_registration_failed")
		return 2
	}
	// Registration is already committed: output failure must never be reported as zero DB writes.
	if e = json.NewEncoder(target).Encode(handoff); e != nil {
		fmt.Fprintln(os.Stderr, "pilot_committed_handoff_unavailable")
		return 3
	}
	if e = target.Sync(); e != nil {
		fmt.Fprintln(os.Stderr, "pilot_committed_handoff_unavailable")
		return 3
	}
	if e = target.Close(); e != nil {
		fmt.Fprintln(os.Stderr, "pilot_committed_handoff_unavailable")
		return 3
	}
	written = true
	if e = json.NewEncoder(os.Stdout).Encode(struct {
		Applied        bool `json:"applied"`
		Recovered      bool `json:"recovered"`
		HandoffWritten bool `json:"handoff_written"`
	}{true, recovered, true}); e != nil {
		return 3
	}
	return 0
}
func main() { os.Exit(run()) }
