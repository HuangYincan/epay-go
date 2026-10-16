package observer

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPairingSharedVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/pairing.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Requests []struct {
			ID, Operation, Body string
			Valid               bool
		}
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors.Requests {
		t.Run(v.ID, func(t *testing.T) {
			_, err := ParsePairing([]byte(v.Body), v.Operation)
			if (err == nil) != v.Valid {
				t.Fatalf("valid=%v error=%v", v.Valid, err)
			}
		})
	}
}
func FuzzPairing(f *testing.F) {
	f.Add([]byte(`{"schema_version":1}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		_, _ = ParsePairing(body, "redeem")
		_, _ = ParsePairing(body, "inspect")
	})
}
