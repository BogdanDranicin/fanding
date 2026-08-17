package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/robots"
)

type fakeRobotSource struct {
	sessions []robots.Session
	feeds    []robots.Feed
}

func (f fakeRobotSource) Snapshot() []robots.Session { return f.sessions }
func (f fakeRobotSource) Feeds() []robots.Feed       { return f.feeds }

func testSource() fakeRobotSource {
	now := time.Date(2026, 8, 17, 15, 30, 0, 0, time.FixedZone("MSK", 3*60*60))
	return fakeRobotSource{
		feeds: []robots.Feed{robots.StockFeed("SBER"), robots.StockFeed("GAZP")},
		sessions: []robots.Session{
			{
				ID: 1, Active: true, DetectedAt: now, UpdatedAt: now,
				Robot: robots.Robot{
					Symbol: "SBER", Side: robots.SideSell,
					QtyMin: 3011, QtyMax: 3013, QtyTypical: 3012,
					PeriodSec: 11.2, Prints: 60, Confidence: 0.8,
					FirstSeen: now.Add(-10 * time.Minute), LastSeen: now,
				},
			},
			{
				ID: 2, Active: false, DetectedAt: now, UpdatedAt: now,
				Robot: robots.Robot{
					Symbol: "GAZP", Side: robots.SideBuy,
					QtyMin: 100, QtyMax: 100, QtyTypical: 100,
					PeriodSec: 30, Prints: 20, Confidence: 0.4,
					FirstSeen: now.Add(-30 * time.Minute), LastSeen: now.Add(-20 * time.Minute),
				},
			},
		},
	}
}

func getRobots(t *testing.T, src RobotSource, query string) robotsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/robots"+query, nil)
	rec := httptest.NewRecorder()
	handleRobots(src)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("статус %d, хотим 200", rec.Code)
	}
	var resp robotsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("разбор ответа: %v (тело %s)", err, rec.Body.String())
	}
	return resp
}

func TestHandleRobotsReturnsAllWithWatchlist(t *testing.T) {
	resp := getRobots(t, testSource(), "")
	if len(resp.Robots) != 2 {
		t.Errorf("роботов %d, хотим 2", len(resp.Robots))
	}
	if len(resp.Watching) != 2 || resp.Watching[0] != "SBER" {
		t.Errorf("Watching = %v, хотим [SBER GAZP]", resp.Watching)
	}
}

func TestHandleRobotsFilters(t *testing.T) {
	src := testSource()
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"по тикеру", "?symbol=sber", 1},
		{"только работающие", "?active=1", 1},
		{"по уверенности", "?min_confidence=0.6", 1},
		{"несуществующий тикер", "?symbol=LKOH", 0},
	}
	for _, tt := range tests {
		if got := len(getRobots(t, src, tt.query).Robots); got != tt.want {
			t.Errorf("%s: роботов %d, хотим %d", tt.name, got, tt.want)
		}
	}
}

// Поля робота лежат в ответе плоско — на них рассчитывает страница.
func TestHandleRobotsJSONShape(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/robots?symbol=SBER", nil)
	rec := httptest.NewRecorder()
	handleRobots(testSource())(rec, req)

	var raw struct {
		Robots []map[string]any `json:"robots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if len(raw.Robots) != 1 {
		t.Fatalf("роботов %d, хотим 1", len(raw.Robots))
	}
	r := raw.Robots[0]
	for _, field := range []string{
		"id", "symbol", "side", "qty_min", "qty_max", "qty_typical",
		"period_sec", "prints", "confidence", "first_seen", "last_seen", "active",
	} {
		if _, ok := r[field]; !ok {
			t.Errorf("в ответе нет поля %q", field)
		}
	}
	if r["side"] != "S" {
		t.Errorf("side = %v, хотим S", r["side"])
	}
}

// Сбор выключен: страница обязана получить пустой список, а не пятисотку.
func TestHandleRobotsWithoutCollector(t *testing.T) {
	resp := getRobots(t, nil, "")
	if len(resp.Robots) != 0 || len(resp.Watching) != 0 {
		t.Errorf("хотим пустой ответ, получили %+v", resp)
	}
}
