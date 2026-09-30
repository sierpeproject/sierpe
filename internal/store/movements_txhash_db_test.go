package store

import (
	"context"
	"testing"
	"time"
)

func testMovementWithHash(transferID, role, txHash string) Movement {
	m := testMovement(transferID, role, "CTOKEN", 100)
	m.ContractID = "CWATCHED"
	m.TxHash = txHash
	return m
}

func TestMovementTxHashReconciliation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE movements, transfers, events, cursor, ledger_hashes`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	rec := LedgerRecord{Sequence: 100, Hash: "aa", PreviousHash: "99", ClosedAt: time.Now().UTC()}
	events := []Event{{
		ID: "0000000000000000300-0000000001", ContractID: "CWATCHED", LedgerSequence: 100,
		ClosedAt: time.Unix(1_700_000_000, 0).UTC(), TxHash: "from-events", TxIndex: 1,
		EventIndex: 1, Topics: []string{}, ValueXDR: "AAAAAQ==", RawXDR: "AAAAAQ==",
	}}
	transfers := []Transfer{testTransfer("0000000000000000200-0000000000", "from-transfers", 0)}
	movements := []Movement{
		// Live path: derived with its hash since 0013.
		testMovementWithHash("0000000000000000100-0000000000", RoleRecipient, "live"),
		// Filled by the transfers join: same event identity.
		testMovementWithHash("0000000000000000200-0000000000", RoleRecipient, ""),
		// Filled by the events join: same transaction, different event.
		testMovementWithHash("0000000000000000300-0000000005", RoleRecipient, ""),
		// Left for the archive resolver; a self-transfer, so two rows.
		testMovementWithHash("0000000000000000400-0000000000", RoleRecipient, ""),
		testMovementWithHash("0000000000000000400-0000000000", RoleSender, ""),
	}
	if err := s.CommitLedger(ctx, "testnet", rec, events, nil, transfers, nil, movements); err != nil {
		t.Fatalf("CommitLedger() error = %v", err)
	}

	fromTransfers, fromEvents, err := s.ResolveMovementTxHashesLocal(ctx, "testnet")
	if err != nil {
		t.Fatalf("ResolveMovementTxHashesLocal() error = %v", err)
	}
	if fromTransfers != 1 || fromEvents != 1 {
		t.Errorf("local passes = %d/%d rows, want 1/1", fromTransfers, fromEvents)
	}

	gaps, err := s.ListMovementTxHashGaps(ctx, "testnet", 10)
	if err != nil {
		t.Fatalf("ListMovementTxHashGaps() error = %v", err)
	}
	if len(gaps) != 1 || gaps[0] != "0000000000000000400" {
		t.Fatalf("gaps = %v, want the one unresolved toid", gaps)
	}

	rows, err := s.SetMovementTxHash(ctx, "testnet", "0000000000000000400", "from-archives")
	if err != nil {
		t.Fatalf("SetMovementTxHash() error = %v", err)
	}
	if rows != 2 {
		t.Errorf("stamped rows = %d, want both attributions of the self-transfer", rows)
	}
	// Idempotent: a second stamp finds nothing NULL to fill.
	rows, err = s.SetMovementTxHash(ctx, "testnet", "0000000000000000400", "from-archives")
	if err != nil {
		t.Fatalf("SetMovementTxHash() second call error = %v", err)
	}
	if rows != 0 {
		t.Errorf("second stamp filled %d rows, want 0", rows)
	}

	gaps, err = s.ListMovementTxHashGaps(ctx, "testnet", 10)
	if err != nil {
		t.Fatalf("ListMovementTxHashGaps() after stamps error = %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("gaps after reconciliation = %v, want none", gaps)
	}

	got, _, err := s.QueryMovements(ctx, "testnet", MovementQuery{ContractID: "CWATCHED", FromLedger: 1, Limit: 10})
	if err != nil {
		t.Fatalf("QueryMovements() error = %v", err)
	}
	want := map[string]string{
		"0000000000000000100-0000000000/recipient": "live",
		"0000000000000000200-0000000000/recipient": "from-transfers",
		"0000000000000000300-0000000005/recipient": "from-events",
		"0000000000000000400-0000000000/recipient": "from-archives",
		"0000000000000000400-0000000000/sender":    "from-archives",
	}
	if len(got) != len(want) {
		t.Fatalf("movements = %d, want %d", len(got), len(want))
	}
	for _, m := range got {
		if w := want[m.TransferID+"/"+m.Role]; m.TxHash != w {
			t.Errorf("movement %s/%s tx_hash = %q, want %q", m.TransferID, m.Role, m.TxHash, w)
		}
	}
}
