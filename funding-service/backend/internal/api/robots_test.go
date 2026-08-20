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
	tapes    []robots.MarketTape
	symbols  []string
	days     []robots.DayVolume
	stream   robots.StreamStatus
}

func (f fakeRobotSource) Snapshot() []robots.Session      { return f.sessions }
func (f fakeRobotSource) Tapes() []robots.MarketTape      { return f.tapes }
func (f fakeRobotSource) Symbols() []string               { return f.symbols }
func (f fakeRobotSource) WatchDescription() string        { return "тестовый отбор" }
func (f fakeRobotSource) DayVolumes() []robots.DayVolume  { return f.days }
func (f fakeRobotSource) StreamStatus() robots.StreamStatus { return f.stream }

func testSource() fakeRobotSource {
	now := time.Date(2026, 8, 17, 15, 30, 0, 0, time.FixedZone("MSK", 3*60*60))
	return fakeRobotSource{
		tapes:   []robots.MarketTape{{Name: "акции TQBR"}},
		stream: robots.StreamStatus{
			Enabled: true, Connected: true, Symbols: 305,
			LastPrintAt: now, LagMs: 1650,
		},
		symbols: []string{"SBER", "GAZP"},
		days: []robots.DayVolume{
			{Symbol: "SBER", Date: "2026-08-17", Buy: 900000, Sell: 750000, Trades: 40000, Since: "09:59:58"},
			{Symbol: "GAZP", Date: "2026-08-17", Buy: 300000, Sell: 310000, Trades: 15000, Since: "09:59:59"},
		},
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

// Живой срез отдаёт только работающих роботов: замолчавший уходит в историю,
// иначе за день список копит сотни отработавших серий.
func TestHandleRobotsReturnsOnlyActive(t *testing.T) {
	resp := getRobots(t, testSource(), "")
	if len(resp.Robots) != 1 {
		t.Fatalf("роботов %d, хотим 1 (замолчавший GAZP не показывается)", len(resp.Robots))
	}
	if resp.Robots[0].Symbol != "SBER" || !resp.Robots[0].Active {
		t.Errorf("в срезе %+v, хотим работающего SBER", resp.Robots[0])
	}
	if len(resp.Watching) != 2 || resp.Watching[0] != "SBER" {
		t.Errorf("Watching = %v, хотим [SBER GAZP]", resp.Watching)
	}
	if len(resp.Tapes) != 1 || resp.Tapes[0] != "акции TQBR" {
		t.Errorf("Tapes = %v, хотим [акции TQBR]", resp.Tapes)
	}
	if resp.WatchRule == "" {
		t.Error("правило отбора должно быть описано: пустой список роботов иначе не отличить от неработающего сбора")
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
		{"по уверенности", "?min_confidence=0.6", 1},
		{"замолчавший не отдаётся", "?symbol=GAZP", 0},
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

// Дневной оборот едет вместе с роботами: без него страница не посчитает силу.
func TestRobotsResponseCarriesDayVolumes(t *testing.T) {
	resp := getRobots(t, testSource(), "")
	if len(resp.DayVolumes) != 2 {
		t.Fatalf("оборотов %d, хотим 2: %+v", len(resp.DayVolumes), resp.DayVolumes)
	}
	var sber robots.DayVolume
	for _, d := range resp.DayVolumes {
		if d.Symbol == "SBER" {
			sber = d
		}
	}
	if sber.Sell != 750000 {
		t.Errorf("продажи SBER = %.0f, хотим 750000", sber.Sell)
	}
	if sber.Since != "09:59:58" {
		t.Errorf("Since = %q: страница показывает, с какого времени считается база", sber.Since)
	}
}

// Состояние быстрого источника едет вместе с роботами: страница по нему пишет,
// откуда пришёл принт и насколько он свежий. Без этого она сообщала бы про
// пятнадцатиминутную задержку ISS и когда сделки идут потоком брокера за секунды.
func TestRobotsResponseCarriesStreamStatus(t *testing.T) {
	resp := getRobots(t, testSource(), "")
	if !resp.Stream.Enabled || !resp.Stream.Connected {
		t.Errorf("Stream = %+v, хотим подключённый быстрый источник", resp.Stream)
	}
	if resp.Stream.Symbols != 305 {
		t.Errorf("инструментов в потоке %d, хотим 305", resp.Stream.Symbols)
	}
	if resp.Stream.LagMs != 1650 {
		t.Errorf("отставание %d мс, хотим 1650", resp.Stream.LagMs)
	}
}

// Сбор выключен — страница обязана понять, что быстрого источника нет, а не
// принять нули за «поток есть, просто молчит».
func TestRobotsStreamStatusWithoutCollector(t *testing.T) {
	if got := getRobots(t, nil, "").Stream; got.Enabled || got.Connected {
		t.Errorf("Stream = %+v, хотим выключенный источник", got)
	}
}
