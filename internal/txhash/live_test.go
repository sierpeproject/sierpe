package txhash

import (
	"context"
	"os"
	"testing"
)

// TestLiveResolveAgainstPublicArchives resolves a pinned mainnet
// transaction from the SDF public archives. The expected side comes from
// the reference, not from this package: Horizon served this transaction
// with this paging_token and this hash (fetched 2026-09-30). Archives are
// immutable, so the pin never rots.
func TestLiveResolveAgainstPublicArchives(t *testing.T) {
	if os.Getenv("SIERPE_LIVE_TEST") == "" {
		t.Skip("SIERPE_LIVE_TEST not set; skipping network-backed test")
	}
	const (
		toid = "0250804104148824064" // ledger 58394881, tx 1
		want = "0ef3829fc6dd3b1302746a470d4336bdb2d2f70747819d5131d011c0954194c6"
	)
	r, err := New([]string{"https://history.stellar.org/prd/core-live/core_live_001"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	got, err := r.Resolve(context.Background(), []string{toid})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got[toid] != want {
		t.Errorf("hash = %q, want the one Horizon serves for this toid", got[toid])
	}
}
