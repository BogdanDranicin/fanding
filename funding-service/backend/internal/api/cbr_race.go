package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/funding-service/backend/internal/source/cbr"
)

// CBRRaceResult holds one channel's outcome in the CBR rate-publication race.
type CBRRaceResult struct {
	SourceID  string  `json:"source_id"`
	Name      string  `json:"name"`
	Desc      string  `json:"desc"`
	RateDate  string  `json:"rate_date"`
	Timestamp string  `json:"timestamp,omitempty"`
	USDRUB    float64 `json:"usd_rub"`
	EURRUB    float64 `json:"eur_rub"`
	CNYRUB    float64 `json:"cny_rub"`
	LatencyMs int64   `json:"latency_ms"`
	Error     string  `json:"error,omitempty"`
}

// cbrRaceClient bypasses the global proxy used by globalAllSpecs.httpClient.
var cbrRaceClient = cbr.DirectClient()

// handleCBRRace polls every known CBR channel in parallel and returns their
// rates and latencies sorted fastest-first — a manual diagnostic for comparing
// which channel publishes a fresh rate first.
func handleCBRRace(w http.ResponseWriter, r *http.Request) {
	channels := cbr.AllChannels()
	resCh := make(chan CBRRaceResult, len(channels))

	var wg sync.WaitGroup
	for _, ch := range channels {
		wg.Add(1)
		go func(c cbr.Channel) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()

			start := time.Now()
			out, err := c.Fetch(ctx, cbrRaceClient)
			res := CBRRaceResult{
				SourceID:  c.ID,
				Name:      c.Name,
				Desc:      c.Desc,
				RateDate:  out.Date,
				Timestamp: out.Timestamp,
				USDRUB:    out.USD,
				EURRUB:    out.EUR,
				CNYRUB:    out.CNY,
				LatencyMs: time.Since(start).Milliseconds(),
			}
			if err != nil {
				res.Error = err.Error()
			}
			resCh <- res
		}(ch)
	}

	go func() { wg.Wait(); close(resCh) }()

	results := make([]CBRRaceResult, 0, len(channels))
	for res := range resCh {
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].LatencyMs < results[j].LatencyMs
	})

	writeJSON(w, http.StatusOK, results)
}
