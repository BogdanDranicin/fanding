package storage

import (
	"context"
	"fmt"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

// InsertSnapshot persists one row per instrument (USDRUBF, EURRUBF, CNYRUBF) for the given snapshot.
func (s *Store) InsertSnapshot(ctx context.Context, snap funding.FundingSnapshot) error {
	const q = `INSERT INTO funding_snapshots
		(timestamp, symbol, vwap, last_price, forex_funding, moex_funding, cb_funding, official_rate)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	type row struct {
		sym string
		inf funding.InstrumentFunding
	}
	instruments := []row{
		{source.SymbolUSDRUBF, snap.USDRUBF},
		{source.SymbolEURRUBF, snap.EURRUBF},
		{source.SymbolCNYRUBF, snap.CNYRUBF},
	}

	for _, r := range instruments {
		if _, err := s.pool.Exec(ctx, q,
			snap.Timestamp, r.sym,
			r.inf.VWAP, r.inf.LastPrice,
			r.inf.ForexFunding, r.inf.MOEXFunding, r.inf.CBFunding, r.inf.OfficialRate,
		); err != nil {
			return fmt.Errorf("insert %s: %w", r.sym, err)
		}
	}
	return nil
}
