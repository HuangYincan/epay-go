package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/epay-go/internal/observerstore"
)

func TestOperatorCLIProcess(t *testing.T) {
	if os.Getenv("OPERATOR_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"operator"}, os.Args[i+1:]...)
			os.Exit(run())
		}
	}
	os.Exit(99)
}
func invoke(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestOperatorCLIProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "OPERATOR_TEST_PROCESS=1", "EPAY_OPERATOR_DSN=host=127.0.0.1 port=1 user=synthetic dbname=synthetic sslmode=disable connect_timeout=1")
	output, e := cmd.CombinedOutput()
	if e == nil {
		return 0, string(output)
	}
	if exit, ok := e.(*exec.ExitError); ok {
		return exit.ExitCode(), string(output)
	}
	t.Fatal("CLI child failed")
	return 99, ""
}
func operatorInput(t *testing.T, review int) string {
	t.Helper()
	in := observerstore.PilotOperatorInput{SchemaVersion: 1, Origin: "https://observer.example", Environment: "test", ReceiverID: "receiver", OrderID: 1, ActorID: 1, ExpiresAtMs: time.Now().UnixMilli() + 300000, ParserVersion: observerstore.PilotOperatorParserVersion, EvidenceRef: "synthetic", TemplateVerified: review, ChatBoundaryVerified: review, IdentityPolicyApproved: review, AmountPolicyApproved: review, NewAPIContractVerified: review}
	body, _ := json.Marshal(in)
	p := filepath.Join(t.TempDir(), "input.json")
	if e := os.WriteFile(p, body, 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestOperatorCLIRequiresReviewBeforeApplyOrDatabase(t *testing.T) {
	input := operatorInput(t, 0)
	output := filepath.Join(t.TempDir(), "handoff.json")
	code, text := invoke(t, "-input", input, "-output", output, "-apply")
	if code != 2 || text != "operator_review_invalid\n" {
		t.Fatal("incomplete review reached storage")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("invalid review created output")
	}
}
func TestOperatorCLIExistingAndSymlinkOutputsNeverTouched(t *testing.T) {
	input := operatorInput(t, 1)
	for _, symlink := range []bool{false, true} {
		d := t.TempDir()
		target := filepath.Join(d, "original")
		output := filepath.Join(d, "handoff.json")
		if e := os.WriteFile(target, []byte("retained"), 0600); e != nil {
			t.Fatal(e)
		}
		if symlink {
			if e := os.Symlink(target, output); e != nil {
				t.Fatal(e)
			}
		} else {
			output = target
		}
		code, text := invoke(t, "-input", input, "-output", output, "-apply")
		if code != 2 || !strings.Contains(text, "operator_output_unavailable") {
			t.Fatal("existing output was not rejected")
		}
		bytes, e := os.ReadFile(target)
		if e != nil || string(bytes) != "retained" {
			t.Fatal("existing artifact changed")
		}
	}
}
func TestOperatorCLIFailureRemovesOnlyOwnEmptyReservation(t *testing.T) {
	input := operatorInput(t, 1)
	output := filepath.Join(t.TempDir(), "handoff.json")
	code, text := invoke(t, "-input", input, "-output", output, "-apply")
	if code != 2 || text != "operator_registration_failed\n" {
		t.Fatal("registration failure was not fixed/sanitized")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("failed reservation survived")
	}
}
