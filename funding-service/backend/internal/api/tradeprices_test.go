package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeISS(t *testing.T) *priceSource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/securities.json":
			w.Write([]byte(`{"securities":{"columns":["secid","primary_boardid"],"data":[["SU26248RMFS3","TQOB"]]}}`))
		case strings.HasSuffix(r.URL.Path, "/candles.json") && q.Get("interval") == "1" &&
			strings.HasPrefix(q.Get("till"), "2026-09-20"):
			w.Write([]byte(`{"candles":{"columns":["close","begin"],"data":[]}}`))
		case strings.HasSuffix(r.URL.Path, "/candles.json") && q.Get("interval") == "60":
			w.Write([]byte(`{"candles":{"columns":["close","begin"],"data":[[276.8,"2026-09-18 22:00:00"],[275.5,"2026-09-18 23:00:00"]]}}`))
		case strings.HasSuffix(r.URL.Path, "/candles.json"):
			price := "278.1"
			switch {
			case strings.Contains(r.URL.Path, "MXZ6"):
				price = "232875"
			case strings.Contains(r.URL.Path, "GDZ6"):
				price = "4400"
			case strings.Contains(r.URL.Path, "SU26248"):
				price = "80.99"
			}
			w.Write([]byte(`{"candles":{"columns":["close","begin"],"data":[[277.9,"2026-09-23 15:36:00"],[` + price +
				`,"2026-09-23 15:37:00"],[999,"2026-09-23 15:38:00"]]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &priceSource{
		base:   srv.URL,
		client: srv.Client(),
		markets: func(context.Context) (map[string]float64, map[string]float64) {
			return map[string]float64{"SBER": 1, "GOLD": 1, "T": 1},
				map[string]float64{"MXZ6": 1, "MXH7": 1, "GDZ6": 1, "MXZ7": 1}
		},
	}
}

func TestPriceAtMessageMinute(t *testing.T) {
	src := fakeISS(t)
	at := time.Date(2026, 9, 23, 12, 37, 8, 0, time.UTC) // 15:37:08 МСК
	cases := map[string]float64{"SBER": 278.1, "MIX": 232875, "MXZ6": 232875, "GOLD": 4400, "ОФЗ 26248": 80.99}
	for ticker, want := range cases {
		got, err := src.priceAt(context.Background(), ticker, at)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v; ждали %v — свеча минуты сообщения, а не следующей", ticker, got, err, want)
		}
	}
}

func TestPriceAtWeekendTakesLastHour(t *testing.T) {
	src := fakeISS(t)
	at := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC) // воскресенье
	got, err := src.priceAt(context.Background(), "SBER", at)
	if err != nil || got != 275.5 {
		t.Fatalf("выходной: %v, %v; ждали закрытие последнего часа пятницы 275.5", got, err)
	}
}

func TestPriceAtUnknownInstrument(t *testing.T) {
	src := fakeISS(t)
	for _, tk := range []string{"BTC", "ETH", "ОФЗ"} {
		if _, err := src.priceAt(context.Background(), tk, time.Now()); !errors.Is(err, errNoInstrument) {
			t.Errorf("%s: ждали errNoInstrument, получили %v", tk, err)
		}
	}
}

func TestFrontFutures(t *testing.T) {
	forts := map[string]float64{"MXZ6": 1, "MXH7": 1, "MXZ7": 1, "MXH8": 1, "BRZL": 1, "BRX6": 1, "BRZ6": 1}
	if got := frontFutures("MX", forts); got != "MXZ6" {
		t.Errorf("MX: %s", got)
	}
	if got := frontFutures("BR", forts); got != "BRX6" {
		t.Errorf("BR: %s", got)
	}
}
