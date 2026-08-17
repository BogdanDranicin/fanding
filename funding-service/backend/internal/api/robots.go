package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/funding-service/backend/internal/robots"
	"github.com/funding-service/backend/internal/storage"
)

// RobotSource — живой срез поиска роботов; *robots.Collector его удовлетворяет.
// Может быть nil: тогда страница работает только по сохранённой истории.
type RobotSource interface {
	Snapshot() []robots.Session
	Feeds() []robots.Feed
}

// robotsResponse — ответ страницы «Роботы».
type robotsResponse struct {
	// Watching — за какими тикерами сейчас следим: без этого пустой список роботов
	// не отличить от неработающего сбора.
	Watching []string         `json:"watching"`
	Robots   []robots.Session `json:"robots"`
	AsOf     time.Time        `json:"as_of"`
}

// handleRobots отдаёт текущих роботов: тех, кто печатает прямо сейчас, и тех,
// кто замолчал недавно.
func handleRobots(src RobotSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := robotsResponse{Robots: []robots.Session{}, Watching: []string{}, AsOf: time.Now()}
		if src == nil {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		for _, f := range src.Feeds() {
			resp.Watching = append(resp.Watching, f.Symbol)
		}

		sessions := src.Snapshot()
		symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
		activeOnly := r.URL.Query().Get("active") == "1"
		minConf := floatParam(r, "min_confidence", 0)

		for _, s := range sessions {
			if symbol != "" && s.Symbol != symbol {
				continue
			}
			if activeOnly && !s.Active {
				continue
			}
			if s.Confidence < minConf {
				continue
			}
			resp.Robots = append(resp.Robots, s)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleRobotsHistory отдаёт сохранённых роботов за период — страница истории.
func handleRobotsHistory(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		days := 1
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
				days = n
			}
		}
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}

		rows, err := store.RecentRobots(r.Context(), storage.RobotFilter{
			Since:         time.Now().Add(-time.Duration(days) * 24 * time.Hour),
			Symbol:        strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol"))),
			MinConfidence: floatParam(r, "min_confidence", 0),
			Limit:         limit,
		})
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		out := make([]robots.Session, 0, len(rows))
		for _, row := range rows {
			out = append(out, robots.SessionOf(row))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func floatParam(r *http.Request, name string, def float64) float64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
