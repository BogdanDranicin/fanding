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
	Tapes() []robots.MarketTape
	WatchDescription() string
	Symbols() []string
	DayVolumes() []robots.DayVolume
	StreamStatus() robots.StreamStatus
}

// robotsResponse — ответ страницы «Роботы».
type robotsResponse struct {
	// Watching — инструменты, по которым сейчас идёт лента. Без этого пустой список
	// роботов не отличить от неработающего сбора.
	Watching []string `json:"watching"`
	// Tapes — какие ленты рынков опрашиваются, WatchRule — чем сужен отбор
	// инструментов внутри них.
	Tapes     []string         `json:"tapes"`
	WatchRule string           `json:"watch_rule"`
	Robots    []robots.Session `json:"robots"`
	// DayVolumes — дневной оборот по каждой ленте, разложенный на покупки и
	// продажи. Страница берёт отсюда базу для силы робота и показывает, с какого
	// времени оборот считается: после перезапуска среди дня база неполная.
	DayVolumes []robots.DayVolume `json:"day_volumes"`
	// Stream — каким источником приходят принты. Страница пишет про свежесть
	// ленты то, что есть на самом деле, а не то, что было верно на прошлом источнике.
	Stream robots.StreamStatus `json:"stream"`
	AsOf   time.Time           `json:"as_of"`
}

// handleRobots отдаёт текущих роботов: тех, кто печатает прямо сейчас, и тех,
// кто замолчал недавно.
func handleRobots(src RobotSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := robotsResponse{
			Robots:     []robots.Session{},
			Watching:   []string{},
			Tapes:      []string{},
			DayVolumes: []robots.DayVolume{},
			AsOf:       time.Now(),
		}
		if src == nil {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		for _, t := range src.Tapes() {
			resp.Tapes = append(resp.Tapes, t.Name)
		}
		resp.Watching = append(resp.Watching, src.Symbols()...)
		resp.WatchRule = src.WatchDescription()
		resp.DayVolumes = append(resp.DayVolumes, src.DayVolumes()...)
		resp.Stream = src.StreamStatus()

		sessions := src.Snapshot()
		symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
		minConf := floatParam(r, "min_confidence", 0)

		for _, s := range sessions {
			// Замолчавший робот со страницы «Сейчас» уходит совсем: его место —
			// в истории, которая читается из базы. Иначе список копит за день
			// сотни отработавших серий и живых в нём не найти.
			if !s.Active {
				continue
			}
			if symbol != "" && s.Symbol != symbol {
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
