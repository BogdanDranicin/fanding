package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/funding-service/backend/internal/robots"
)

// UpsertRobot сохраняет сессию робота: строка с нулевым ID вставляется, остальные
// обновляются по своему ID. Возвращает ID строки — коллектор запоминает его, чтобы
// следующее обновление той же серии не завело в базе вторую запись.
func (s *Store) UpsertRobot(ctx context.Context, in robots.RobotRow) (int64, error) {
	if in.ID == 0 {
		const q = `
			INSERT INTO robots
				(symbol, side, qty_min, qty_max, qty_typical, period_sec, jitter,
				 prints, beats, confidence, price_first, price_last,
				 first_seen, last_seen, detected_at, updated_at, active,
				 hour_lots, day_side_lots)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
			RETURNING id`
		var id int64
		err := s.pool.QueryRow(ctx, q,
			in.Symbol, in.Side, in.QtyMin, in.QtyMax, in.QtyTypical, in.PeriodSec, in.Jitter,
			in.Prints, in.Beats, in.Confidence, in.PriceFirst, in.PriceLast,
			in.FirstSeen, in.LastSeen, in.DetectedAt, in.UpdatedAt, in.Active,
			in.HourLots, in.DaySideLots,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("insert robot: %w", err)
		}
		return id, nil
	}

	// detected_at и first_seen у живой строки не трогаем: это начало серии, и оно
	// не должно уезжать вперёд вместе со скользящим окном анализа.
	const q = `
		UPDATE robots SET
			qty_min     = LEAST(qty_min, $2),
			qty_max     = GREATEST(qty_max, $3),
			qty_typical = $4,
			period_sec  = $5,
			jitter      = $6,
			prints      = $7,
			beats       = $8,
			confidence  = $9,
			price_last  = $10,
			last_seen   = GREATEST(last_seen, $11),
			updated_at  = $12,
			active      = $13,
			-- Часовой оборот берём наибольший за жизнь серии: он меряется на
			-- скользящем окне и к вечеру спадает, а сила робота должна отражать
			-- рынок, в котором он работал, а не последнюю тихую минуту.
			hour_lots     = GREATEST(robots.hour_lots, $14),
			day_side_lots = GREATEST(robots.day_side_lots, $15)
		WHERE id = $1`
	_, err := s.pool.Exec(ctx, q,
		in.ID, in.QtyMin, in.QtyMax, in.QtyTypical, in.PeriodSec, in.Jitter,
		in.Prints, in.Beats, in.Confidence, in.PriceLast,
		in.LastSeen, in.UpdatedAt, in.Active, in.HourLots, in.DaySideLots,
	)
	if err != nil {
		return 0, fmt.Errorf("update robot %d: %w", in.ID, err)
	}
	return in.ID, nil
}

// RobotFilter — отбор для страницы истории.
type RobotFilter struct {
	Since         time.Time // не старше этого момента по last_seen; нулевое — без границы
	Symbol        string    // пусто — все тикеры
	MinConfidence float64
	Limit         int
}

// RecentRobots отдаёт сохранённых роботов, свежие первыми.
func (s *Store) RecentRobots(ctx context.Context, f RobotFilter) ([]robots.RobotRow, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	const q = `
		SELECT id, symbol, side, qty_min, qty_max, qty_typical, period_sec, jitter,
		       prints, beats, confidence, price_first, price_last,
		       first_seen, last_seen, detected_at, updated_at, active,
		       hour_lots, day_side_lots
		FROM robots
		WHERE ($1::timestamptz IS NULL OR last_seen >= $1)
		  AND ($2::text IS NULL OR symbol = $2)
		  AND confidence >= $3
		ORDER BY last_seen DESC
		LIMIT $4`

	var since *time.Time
	if !f.Since.IsZero() {
		since = &f.Since
	}
	var symbol *string
	if f.Symbol != "" {
		symbol = &f.Symbol
	}

	rows, err := s.pool.Query(ctx, q, since, symbol, f.MinConfidence, limit)
	if err != nil {
		return nil, fmt.Errorf("select robots: %w", err)
	}
	defer rows.Close()

	var out []robots.RobotRow
	for rows.Next() {
		var r robots.RobotRow
		if err := rows.Scan(
			&r.ID, &r.Symbol, &r.Side, &r.QtyMin, &r.QtyMax, &r.QtyTypical, &r.PeriodSec, &r.Jitter,
			&r.Prints, &r.Beats, &r.Confidence, &r.PriceFirst, &r.PriceLast,
			&r.FirstSeen, &r.LastSeen, &r.DetectedAt, &r.UpdatedAt, &r.Active,
			&r.HourLots, &r.DaySideLots,
		); err != nil {
			return nil, fmt.Errorf("scan robot: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
