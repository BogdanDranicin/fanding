package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// SnapshotRow is one row from funding_snapshots joined per-timestamp.
type SnapshotRow struct {
	Timestamp    time.Time
	Symbol       string
	VWAP         *float64
	LastPrice    *float64
	ForexFunding *float64
	MOEXFunding  *float64
	CBFunding    *float64
	OfficialRate *float64
}

// RecentSnapshots returns the latest N rows (all symbols) ordered by timestamp desc.
func (s *Store) RecentSnapshots(ctx context.Context, limit int) ([]SnapshotRow, error) {
	const q = `
		SELECT timestamp, symbol, vwap, last_price, forex_funding, moex_funding, cb_funding, official_rate
		FROM funding_snapshots
		ORDER BY timestamp DESC
		LIMIT $1`

	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SnapshotRow
	for rows.Next() {
		var r SnapshotRow
		if err := rows.Scan(&r.Timestamp, &r.Symbol, &r.VWAP, &r.LastPrice,
			&r.ForexFunding, &r.MOEXFunding, &r.CBFunding, &r.OfficialRate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CBPublicationRow is one row from cb_publications.
type CBPublicationRow struct {
	Date       time.Time
	USDRate    *float64
	EURRate    *float64
	DetectedAt *time.Time
}

// RecentCBPublications returns publications from the last N days.
func (s *Store) RecentCBPublications(ctx context.Context, days int) ([]CBPublicationRow, error) {
	const q = `
		SELECT date, usd_rate, eur_rate, detected_at
		FROM cb_publications
		WHERE date >= current_date - ($1::int || ' days')::interval
		ORDER BY date DESC`

	rows, err := s.pool.Query(ctx, q, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CBPublicationRow
	for rows.Next() {
		var r CBPublicationRow
		if err := rows.Scan(&r.Date, &r.USDRate, &r.EURRate, &r.DetectedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserRecord is returned by CreateUser.
type UserRecord struct {
	ID        int64
	LinkToken string
}

// CreateUser inserts a new user row with a random 32-hex-char link_token.
func (s *Store) CreateUser(ctx context.Context) (UserRecord, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return UserRecord{}, err
	}
	token := hex.EncodeToString(buf)

	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (link_token) VALUES ($1) RETURNING id`,
		token,
	).Scan(&id)
	if err != nil {
		return UserRecord{}, err
	}
	return UserRecord{ID: id, LinkToken: token}, nil
}

// UserByIDAndToken verifies ownership: returns linked status only if id+token match.
// Returns pgx.ErrNoRows (wrapped) if the user is not found or the token is wrong.
func (s *Store) UserByIDAndToken(ctx context.Context, id int64, token string) (linked bool, err error) {
	var chatID *int64
	err = s.pool.QueryRow(ctx,
		`SELECT telegram_chat_id FROM users WHERE id = $1 AND link_token = $2`,
		id, token,
	).Scan(&chatID)
	if err != nil {
		return false, err
	}
	return chatID != nil, nil
}
