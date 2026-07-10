package storage

import (
	"context"
	"time"
)

// CBPublicationInput is the audit payload for one CBR-publication day.
// All value fields are pointers: a nil field is passed as SQL NULL and — via the
// COALESCE rules in UpsertCBPublication — leaves the stored value untouched.
type CBPublicationInput struct {
	Date       time.Time  // MSK calendar date of the publication event (primary key)
	DetectedAt *time.Time // exact moment we first saw the new rate (set once)
	UpdatedAt  time.Time  // last time this row was touched

	USDRate *float64
	EURRate *float64
	CNYRate *float64

	CBFundingUSD *float64 // converges to the final MOEX SWAPRATE through the evening
	CBFundingEUR *float64
	CNYFunding   *float64

	PredictedFundingUSD *float64 // live forecast captured just before publication
	PredictedFundingEUR *float64
	PredictedCBRateUSD  *float64
	PredictedCBRateEUR  *float64

	WinnerChannel   *string // race winner at detection (set once)
	WinnerLatencyMs *int64
}

// UpsertCBPublication writes or updates the audit row for one publication day.
//
// Column update policy on conflict:
//   - detected_at, winner_channel, winner_latency_ms: set once (first non-NULL wins) —
//     they describe the moment of publication and must not drift on later re-fixes.
//   - rates, cb_funding, predicted_*: take the newest non-NULL value, so the row
//     converges on the final numbers (e.g. cb_funding → MOEX SWAPRATE at clearing,
//     predicted_* → the last live forecast before the rate was published).
//   - updated_at: always overwritten.
func (s *Store) UpsertCBPublication(ctx context.Context, in CBPublicationInput) error {
	const q = `
		INSERT INTO cb_publications
			(date, detected_at, updated_at, usd_rate, eur_rate, cny_rate,
			 cb_funding_usd, cb_funding_eur, cny_funding,
			 predicted_funding_usd, predicted_funding_eur,
			 predicted_cb_rate_usd, predicted_cb_rate_eur,
			 winner_channel, winner_latency_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (date) DO UPDATE SET
			detected_at           = COALESCE(cb_publications.detected_at, EXCLUDED.detected_at),
			updated_at            = EXCLUDED.updated_at,
			usd_rate              = COALESCE(EXCLUDED.usd_rate, cb_publications.usd_rate),
			eur_rate              = COALESCE(EXCLUDED.eur_rate, cb_publications.eur_rate),
			cny_rate              = COALESCE(EXCLUDED.cny_rate, cb_publications.cny_rate),
			cb_funding_usd        = COALESCE(EXCLUDED.cb_funding_usd, cb_publications.cb_funding_usd),
			cb_funding_eur        = COALESCE(EXCLUDED.cb_funding_eur, cb_publications.cb_funding_eur),
			cny_funding           = COALESCE(EXCLUDED.cny_funding, cb_publications.cny_funding),
			predicted_funding_usd = COALESCE(EXCLUDED.predicted_funding_usd, cb_publications.predicted_funding_usd),
			predicted_funding_eur = COALESCE(EXCLUDED.predicted_funding_eur, cb_publications.predicted_funding_eur),
			predicted_cb_rate_usd = COALESCE(EXCLUDED.predicted_cb_rate_usd, cb_publications.predicted_cb_rate_usd),
			predicted_cb_rate_eur = COALESCE(EXCLUDED.predicted_cb_rate_eur, cb_publications.predicted_cb_rate_eur),
			winner_channel        = COALESCE(cb_publications.winner_channel, EXCLUDED.winner_channel),
			winner_latency_ms     = COALESCE(cb_publications.winner_latency_ms, EXCLUDED.winner_latency_ms)`

	_, err := s.pool.Exec(ctx, q,
		in.Date, in.DetectedAt, in.UpdatedAt, in.USDRate, in.EURRate, in.CNYRate,
		in.CBFundingUSD, in.CBFundingEUR, in.CNYFunding,
		in.PredictedFundingUSD, in.PredictedFundingEUR,
		in.PredictedCBRateUSD, in.PredictedCBRateEUR,
		in.WinnerChannel, in.WinnerLatencyMs,
	)
	return err
}
