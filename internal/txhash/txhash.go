// Package txhash resolves the transaction hash behind a movement id from
// the public history archives. The archives are the permanent record, so
// history far below any RPC or Horizon retention still resolves (the SDF
// public Horizon keeps weeks of history, not years), and no captive core
// is involved: each checkpoint's results file pairs every transaction
// hash with its ledger in application order, which is exactly the
// coordinate system the {toid}-{event_index} ids use.
//
// Trust: the hashes come from the same public archives the archive leg
// replays from — the source this appliance already gates with a
// byte-equivalence proof against its RPC before trusting a heal.
package txhash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/stellar/go-stellar-sdk/historyarchive"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// checkpointLedgers is the archive checkpoint frequency: one results file
// per 64 ledgers, published at sequences that are 63 mod 64.
const checkpointLedgers = 64

// maxCachedLedgers bounds the parsed-results cache. A reconciliation
// walks toids in chain order, so eviction hardly ever hurts; the cap only
// keeps a long-lived Resolver from growing without bound.
const maxCachedLedgers = 1 << 14

// archive is the slice of historyarchive.ArchiveInterface this package
// consumes (rule 5).
type archive interface {
	GetXdrStream(path string) (*xdr.Stream, error)
}

// Resolver maps toids to transaction hashes through checkpoint results
// files. One Resolver caches the checkpoints it has parsed, so a batch of
// toids from the same era costs one archive fetch per checkpoint, not one
// per transaction. Safe for concurrent use.
type Resolver struct {
	archives []archive

	mu sync.Mutex
	// ledgers holds parsed results: ledger sequence → tx hashes in
	// application order. A ledger that closed empty holds nil.
	ledgers map[uint32][]string
}

// New connects to the given history archives, in failover order.
func New(urls []string) (*Resolver, error) {
	if len(urls) == 0 {
		return nil, errors.New("txhash: no history archives configured")
	}
	r := &Resolver{ledgers: make(map[uint32][]string)}
	for _, u := range urls {
		a, err := historyarchive.Connect(u, historyarchive.ArchiveOptions{})
		if err != nil {
			return nil, fmt.Errorf("txhash: connect archive %s: %w", u, err)
		}
		r.archives = append(r.archives, a)
	}
	return r, nil
}

// newWithArchives wires fakes in tests.
func newWithArchives(archives ...archive) *Resolver {
	return &Resolver{archives: archives, ledgers: make(map[uint32][]string)}
}

// Resolve maps each toid to its transaction hash. A toid it cannot
// resolve is absent from the result, and err carries the first failure —
// a checkpoint the archives have not published yet is expected for very
// recent rows, and a fetch failure is transient; either way the remaining
// toids simply wait for the next reconciliation call.
func (r *Resolver) Resolve(ctx context.Context, toids []string) (map[string]string, error) {
	out := make(map[string]string, len(toids))
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, toid := range toids {
		if err := ctx.Err(); err != nil {
			fail(err)
			break
		}
		ledger, txIndex, err := parseTOID(toid)
		if err != nil {
			fail(err)
			continue
		}
		hashes, err := r.ledgerHashes(ledger)
		if err != nil {
			fail(err)
			continue
		}
		if txIndex < 1 || int(txIndex) > len(hashes) {
			fail(fmt.Errorf("txhash: toid %s names transaction %d of a ledger with %d", toid, txIndex, len(hashes)))
			continue
		}
		out[toid] = hashes[txIndex-1]
	}
	return out, firstErr
}

// parseTOID splits a 19-digit toid into its ledger sequence and 1-based
// transaction application index (TOID layout: 32 bits ledger, 20 bits
// transaction, 12 bits operation).
func parseTOID(toid string) (ledger uint32, txIndex uint32, err error) {
	if len(toid) != 19 {
		return 0, 0, fmt.Errorf("txhash: toid %q is not a 19-digit id prefix", toid)
	}
	v, err := strconv.ParseInt(toid, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("txhash: toid %q is not numeric", toid)
	}
	return uint32(v >> 32), uint32((v >> 12) & 0xFFFFF), nil
}

// ledgerHashes returns the ledger's transaction hashes in application
// order, fetching and caching its whole checkpoint on a miss.
func (r *Resolver) ledgerHashes(ledger uint32) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hashes, ok := r.ledgers[ledger]; ok {
		return hashes, nil
	}
	if len(r.ledgers) > maxCachedLedgers {
		r.ledgers = make(map[uint32][]string)
	}

	checkpoint := ledger | (checkpointLedgers - 1)
	path := historyarchive.CategoryCheckpointPath("results", checkpoint)
	var lastErr error
	for _, a := range r.archives {
		stream, err := a.GetXdrStream(path)
		if err != nil {
			lastErr = fmt.Errorf("txhash: fetch %s: %w", path, err)
			continue
		}
		err = r.cacheStream(stream, checkpoint)
		stream.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return r.ledgers[ledger], nil
	}
	return nil, lastErr
}

// cacheStream parses one checkpoint's results file into the cache. Every
// ledger of the checkpoint range is filled — the ones absent from the
// file closed empty — so the whole range is answered by this one fetch.
func (r *Resolver) cacheStream(stream *xdr.Stream, checkpoint uint32) error {
	seen := make(map[uint32][]string, checkpointLedgers)
	for {
		var entry xdr.TransactionHistoryResultEntry
		if err := stream.ReadOne(&entry); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("txhash: read results of checkpoint %d: %w", checkpoint, err)
		}
		results := entry.TxResultSet.Results
		hashes := make([]string, len(results))
		for i, res := range results {
			hashes[i] = res.TransactionHash.HexString()
		}
		seen[uint32(entry.LedgerSeq)] = hashes
	}
	first := checkpoint - (checkpointLedgers - 1)
	// The genesis checkpoint starts at ledger 1, not at 0.
	if checkpoint == checkpointLedgers-1 {
		first = 1
	}
	for seq := first; seq <= checkpoint; seq++ {
		r.ledgers[seq] = seen[seq]
	}
	return nil
}
