package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/funding-service/backend/internal/source"
)

// BatchInsertTicks bulk-inserts ticks using PostgreSQL COPY protocol for maximum throughput.
func (s *Store) BatchInsertTicks(ctx context.Context, ticks []source.Tick) error {
	if len(ticks) == 0 {
		return nil
	}

	rows := make([][]any, len(ticks))
	for i, t := range ticks {
		rows[i] = []any{t.Timestamp, t.Symbol, t.Price, t.Volume, kindStr(t.Kind), t.Source}
	}

	_, err := s.pool.CopyFrom(
		ctx,
		pgx.Identifier{"ticks"},
		[]string{"timestamp", "symbol", "price", "volume", "kind", "source"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("CopyFrom ticks: %w", err)
	}
	return nil
}

func kindStr(k source.TickKind) string {
	switch k {
	case source.KindLastPrice:
		return "last"
	case source.KindBid:
		return "bid"
	case source.KindAsk:
		return "ask"
	case source.KindOfficialRate:
		return "official_rate"
	case source.KindVWAP:
		return "vwap"
	default:
		return "unknown"
	}
}
