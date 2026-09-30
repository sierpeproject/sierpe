package store

import (
	"context"
	"fmt"
)

// The tx-hash reconciliation (admin doctrine, rule 11) fills tx_hash on
// movement rows derived before migration 0013. The local passes come
// first: the transfers row sharing the event identity, then any events
// row of the same transaction — both carried their hash through the same
// distrusted ingest path as the movement itself. Whatever neither pass
// reaches, the caller resolves from the history archives and stamps per
// transaction with SetMovementTxHash. Every statement only ever fills
// NULLs, so the whole reconciliation can be re-run freely.

// ResolveMovementTxHashesLocal fills tx_hash from rows this database
// already trusts and reports how many rows each pass filled.
func (s *Store) ResolveMovementTxHashesLocal(ctx context.Context, network string) (fromTransfers, fromEvents int64, err error) {
	t, err := s.pool.Exec(ctx, `
		UPDATE movements m SET tx_hash = t.tx_hash
		FROM transfers t
		WHERE m.network = $1 AND m.tx_hash IS NULL
		  AND t.network = m.network AND t.id = m.transfer_id`, network)
	if err != nil {
		return 0, 0, fmt.Errorf("store: resolve movement tx hashes from transfers: %w", err)
	}
	// A transaction with several events matches several rows; they all
	// carry the same hash, so which one the join picks does not matter.
	e, err := s.pool.Exec(ctx, `
		UPDATE movements m SET tx_hash = e.tx_hash
		FROM events e
		WHERE m.network = $1 AND m.tx_hash IS NULL
		  AND e.network = m.network AND left(e.id, 19) = left(m.transfer_id, 19)`, network)
	if err != nil {
		return t.RowsAffected(), 0, fmt.Errorf("store: resolve movement tx hashes from events: %w", err)
	}
	return t.RowsAffected(), e.RowsAffected(), nil
}

// ListMovementTxHashGaps returns the distinct toids (the 19-digit id
// prefix naming one transaction) of movements still missing tx_hash, in
// chain order, up to limit.
func (s *Store) ListMovementTxHashGaps(ctx context.Context, network string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT left(transfer_id, 19)
		FROM movements
		WHERE network = $1 AND tx_hash IS NULL
		ORDER BY 1
		LIMIT $2`, network, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list movement tx hash gaps: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var toid string
		if err := rows.Scan(&toid); err != nil {
			return nil, fmt.Errorf("store: scan movement tx hash gap: %w", err)
		}
		out = append(out, toid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list movement tx hash gaps: %w", err)
	}
	return out, nil
}

// SetMovementTxHash stamps every still-unset movement of one transaction
// and reports how many rows it filled.
func (s *Store) SetMovementTxHash(ctx context.Context, network, toid, txHash string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE movements SET tx_hash = $3
		WHERE network = $1 AND tx_hash IS NULL AND left(transfer_id, 19) = $2`,
		network, toid, txHash)
	if err != nil {
		return 0, fmt.Errorf("store: set movement tx hash: %w", err)
	}
	return tag.RowsAffected(), nil
}
