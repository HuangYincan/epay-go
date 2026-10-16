//go:build observersynthetic

package observerstore_test

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"regexp"
	"strconv"
	"testing"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMatchCrashHelper(t *testing.T) {
	schema := os.Getenv("OBSERVER_MATCH_CHILD_SCHEMA")
	if schema == "" {
		return
	}
	if !regexp.MustCompile(`^observer_test_[0-9]+$`).MatchString(schema) {
		t.Fatal("invalid child schema")
	}
	cfg, err := pgx.ParseConfig(os.Getenv("EPAY_TEST_DATABASE_DSN"))
	if err != nil {
		t.Fatal("invalid child database configuration")
	}
	cfg.RuntimeParams["search_path"] = schema
	cfg.RuntimeParams["statement_timeout"] = "10000"
	pool := stdlib.OpenDB(*cfg)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("child database connection failed")
	}
	id, err := strconv.ParseInt(os.Getenv("OBSERVER_MATCH_CHILD_EVENT"), 10, 64)
	if err != nil {
		t.Fatal("child event id")
	}
	phase := os.Getenv("OBSERVER_MATCH_CHILD_PHASE")
	if phase == "before-commit" {
		if err := db.Callback().Create().After("gorm:create").Register("test:crash_after_income_insert", func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*model.BalanceRecord); ok && tx.Error == nil {
				os.Exit(42)
			}
		}); err != nil {
			t.Fatal("child crash hook")
		}
	}
	matcher, err := observerstore.NewMatcher(db, "test")
	if err != nil {
		t.Fatal("child matcher")
	}
	if _, err := matcher.ProcessEvent(context.Background(), id, pairingNow+1000); err != nil {
		t.Fatal("child processing failed")
	}
	if phase == "after-commit" {
		os.Exit(42)
	}
	t.Fatal("child did not cross intended crash boundary")
}
func TestMatchActualProcessExitBeforeAndAfterCommit(t *testing.T) {
	for _, phase := range []string{"before-commit", "after-commit"} {
		t.Run(phase, func(t *testing.T) {
			f := newMatchFixture(t, "wxpay")
			f.order(t, "crash", pairingNow)
			id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
			var schema string
			f.db.Raw(`SELECT current_schema()`).Scan(&schema)
			command := osexec.Command(os.Args[0], "-test.run=^TestMatchCrashHelper$")
			command.Env = append(os.Environ(), "OBSERVER_MATCH_CHILD_SCHEMA="+schema, "OBSERVER_MATCH_CHILD_EVENT="+strconv.FormatInt(id, 10), "OBSERVER_MATCH_CHILD_PHASE="+phase)
			output, err := command.CombinedOutput()
			var exit *osexec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 42 {
				t.Fatalf("child failed to exit at boundary: %v %s", err, output)
			}
			if phase == "before-commit" {
				f.assertIncome(t, 0)
				if count(t, f.db, "observer_match_decisions") != 0 {
					t.Fatal("uncommitted decision survived process exit")
				}
			} else {
				f.assertIncome(t, 1)
			}
			restarted, err := observerstore.NewMatcher(f.db, "test")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.ProcessEvent(context.Background(), id, pairingNow+1000); err != nil {
				t.Fatal(err)
			}
			f.assertIncome(t, 1)
		})
	}
}
