package txhash

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/historyarchive"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// toidFor builds the 19-digit id prefix the store derives for a
// transaction (TOID layout: ledger << 32 | txIndex << 12).
func toidFor(ledger, txIndex uint32) string {
	return fmt.Sprintf("%019d", int64(ledger)<<32|int64(txIndex)<<12)
}

// hashFor is a deterministic fake transaction hash.
func hashFor(ledger, txIndex uint32) xdr.Hash {
	var h xdr.Hash
	h[0], h[1] = byte(ledger), byte(txIndex)
	return h
}

// resultsFile frames TransactionHistoryResultEntry records the way the
// archives do: 4-byte big-endian length with the high bit set, then the
// XDR body, the whole stream gzipped.
func resultsFile(t *testing.T, txCounts map[uint32]int) []byte {
	t.Helper()
	var raw bytes.Buffer
	for ledger, n := range txCounts {
		results := make([]xdr.TransactionResultPair, n)
		for i := range results {
			results[i] = xdr.TransactionResultPair{
				TransactionHash: hashFor(ledger, uint32(i+1)),
				Result: xdr.TransactionResult{
					Result: xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxTooEarly},
				},
			}
		}
		entry := xdr.TransactionHistoryResultEntry{
			LedgerSeq:   xdr.Uint32(ledger),
			TxResultSet: xdr.TransactionResultSet{Results: results},
		}
		body, err := entry.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		var frame [4]byte
		binary.BigEndian.PutUint32(frame[:], uint32(len(body))|0x80000000)
		raw.Write(frame[:])
		raw.Write(body)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return gz.Bytes()
}

// fakeArchive serves canned checkpoint files and counts fetches.
type fakeArchive struct {
	files   map[string][]byte
	fetches int
	err     error
}

func (f *fakeArchive) GetXdrStream(path string) (*xdr.Stream, error) {
	f.fetches++
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("404: %s", path)
	}
	return xdr.NewGzStream(io.NopCloser(bytes.NewReader(b)))
}

func resultsPath(checkpoint uint32) string {
	return historyarchive.CategoryCheckpointPath("results", checkpoint)
}

func TestResolveFindsHashesByApplicationOrder(t *testing.T) {
	// Ledgers 200 and 201 share checkpoint 255; 202 closed empty.
	a := &fakeArchive{files: map[string][]byte{
		resultsPath(255): resultsFile(t, map[uint32]int{200: 3, 201: 1}),
	}}
	r := newWithArchives(a)

	toids := []string{toidFor(200, 3), toidFor(201, 1), toidFor(200, 1)}
	got, err := r.Resolve(context.Background(), toids)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("resolved = %v", got)
	}
	if got[toidFor(200, 3)] != hashFor(200, 3).HexString() {
		t.Errorf("ledger 200 tx 3 = %s, want the third hash in application order", got[toidFor(200, 3)])
	}
	if got[toidFor(201, 1)] != hashFor(201, 1).HexString() {
		t.Errorf("ledger 201 tx 1 = %s", got[toidFor(201, 1)])
	}
	if a.fetches != 1 {
		t.Errorf("fetches = %d: one checkpoint must be fetched once, not per toid", a.fetches)
	}
}

func TestResolveReportsWhatItCannotName(t *testing.T) {
	a := &fakeArchive{files: map[string][]byte{
		resultsPath(255): resultsFile(t, map[uint32]int{200: 1}),
	}}
	r := newWithArchives(a)

	// Ledger 202 is inside the fetched checkpoint but closed empty: the
	// toid names a transaction that never existed.
	got, err := r.Resolve(context.Background(), []string{toidFor(202, 1), toidFor(200, 1)})
	if err == nil || !strings.Contains(err.Error(), "ledger with 0") {
		t.Errorf("err = %v, want the empty-ledger failure", err)
	}
	if _, ok := got[toidFor(202, 1)]; ok {
		t.Error("an unresolvable toid must be absent from the result")
	}
	if got[toidFor(200, 1)] == "" {
		t.Error("one failure must not sink the resolvable toids")
	}

	// A malformed toid fails without touching the archives.
	if _, err := r.Resolve(context.Background(), []string{"not-a-toid"}); err == nil {
		t.Error("malformed toid must error")
	}
}

func TestResolveFailsOverBetweenArchives(t *testing.T) {
	file := resultsFile(t, map[uint32]int{200: 1})
	broken := &fakeArchive{err: errors.New("connection refused")}
	healthy := &fakeArchive{files: map[string][]byte{resultsPath(255): file}}
	r := newWithArchives(broken, healthy)

	got, err := r.Resolve(context.Background(), []string{toidFor(200, 1)})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got[toidFor(200, 1)] != hashFor(200, 1).HexString() {
		t.Errorf("resolved = %v", got)
	}

	// A checkpoint nobody serves surfaces the fetch failure and resolves
	// nothing — the caller retries on a later reconciliation.
	missing, err := r.Resolve(context.Background(), []string{toidFor(1000, 1)})
	if err == nil || len(missing) != 0 {
		t.Errorf("missing checkpoint: got %v, err %v", missing, err)
	}
}
