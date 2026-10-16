//go:build observersynthetic

package observerstore_test

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/service"
)

func TestMatchedCallbackTLSRecoveryAndScope(t *testing.T) {
	for _, provider := range []string{"wxpay", "alipay"} {
		t.Run(provider, func(t *testing.T) {
			f := newMatchFixture(t, provider)
			oldDB := database.DB
			database.DB = f.db
			t.Cleanup(func() { database.DB = oldDB })
			var hits atomic.Int32
			var mode atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Path != "/api/user/epay/notify" || r.URL.Query().Get("money") != "10" || r.URL.Query().Get("out_trade_no") != "callback" || r.URL.Query().Get("type") != provider || r.URL.Query().Get("api_trade_no") != "" {
					t.Error("callback did not preserve original order")
				}
				if mode.Load() == 0 {
					http.Error(w, "fail", http.StatusServiceUnavailable)
					return
				}
				w.Write([]byte("success"))
			}))
			defer server.Close()
			ca := filepath.Join(t.TempDir(), "callback-ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			endpoint := server.URL + "/api/user/epay/notify"
			t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_URL", endpoint)
			t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE", ca)
			o := f.order(t, "callback", pairingNow)
			exec(t, f.db, `UPDATE orders SET notify_url=? WHERE id=?`, endpoint, o.ID)
			id, _ := f.receive(t, pairingNow+1000, pairingNow+1000, nil)
			f.process(t, id, pairingNow+1000, "matched")
			if err := service.NewNotifyService().SendNotify(o); err != nil {
				t.Fatal(err)
			}
			var stored model.Order
			if err := f.db.First(&stored, o.ID).Error; err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 1 || stored.NotifyStatus != model.NotifyStatusSending || stored.NextNotifyAt == nil || stored.NotifyCount != 1 {
				t.Fatal("failed TLS callback not persisted for retry")
			}
			// Stop/resume the test scope; a new worker must recheck its current scope.
			exec(t, f.db, `UPDATE observer_synthetic_merchants SET enabled=false WHERE merchant_id=?`, o.MerchantID)
			exec(t, f.db, `UPDATE orders SET next_notify_at=? WHERE id=?`, time.Now().Add(-time.Second), o.ID)
			if err := service.NewNotifyService().SendNotify(o); err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 1 {
				t.Fatal("disabled scope reached network")
			}
			exec(t, f.db, `UPDATE observer_synthetic_merchants SET enabled=true WHERE merchant_id=?`, o.MerchantID)
			exec(t, f.db, `UPDATE orders SET next_notify_at=? WHERE id=?`, time.Now().Add(-time.Second), o.ID)
			mode.Store(1)
			if err := service.NewNotifyService().SendNotify(o); err != nil {
				t.Fatal(err)
			}
			stored = model.Order{}
			if err := f.db.First(&stored, o.ID).Error; err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 2 || stored.NotifyStatus != model.NotifyStatusSuccess || stored.NotifyCount != 3 || stored.NextNotifyAt != nil || stored.NotifyLeaseUntil != nil {
				t.Fatalf("new worker callback state: hits=%d status=%d count=%d next=%v lease=%v", hits.Load(), stored.NotifyStatus, stored.NotifyCount, stored.NextNotifyAt, stored.NotifyLeaseUntil)
			}
			if err := service.NewNotifyService().SendNotify(o); err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 2 {
				t.Fatal("successful callback repeated")
			}
			f.assertIncome(t, 1)
		})
	}
}
