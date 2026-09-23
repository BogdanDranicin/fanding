package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTradeIngestNeedsToken(t *testing.T) {
	body := `{"channel_id":-100,"channel_title":"x","msg_id":1,"date":"2026-09-23T10:00:00Z","text":"Купил Сбер"}`
	cases := []struct {
		name, server, header string
	}{
		{"приём выключен без секрета на сервере", "", ""},
		{"пустой заголовок", "s3cret", ""},
		{"чужой секрет", "s3cret", "other"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/trades/message", strings.NewReader(body))
		if c.header != "" {
			req.Header.Set("X-Trades-Token", c.header)
		}
		rec := httptest.NewRecorder()
		handleTradeIngest(nil, c.server)(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: код %d, ждали 403", c.name, rec.Code)
		}
	}
}

func TestTradePositionValidation(t *testing.T) {
	ok := tradePositionReq{Author: "Goodwin", Ticker: "SBER", Direction: "long", Status: "open"}
	if _, err := ok.position(); err != nil {
		t.Fatalf("корректная позиция отклонена: %v", err)
	}
	bad := []tradePositionReq{
		{Ticker: "SBER", Direction: "long", Status: "open"},
		{Author: "a", Ticker: "SBER", Direction: "up", Status: "open"},
		{Author: "a", Ticker: "SBER", Direction: "long", Status: "maybe"},
		{Author: "a", Ticker: strings.Repeat("x", 121), Direction: "long", Status: "open"},
	}
	for i, b := range bad {
		if _, err := b.position(); err == nil {
			t.Errorf("случай %d должен быть отклонён", i)
		}
	}
}
