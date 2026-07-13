package store

import "context"

// LatestEventSeq returns the sequence number of the most recently persisted
// event (0 when the log is empty). The SSE handler uses it to start a fresh
// connection's replay near the tail of the log instead of from seq 0 — a
// dashboard opened weeks into a project must not re-receive the entire
// persisted history.
func (s *Store) LatestEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	return seq, err
}
