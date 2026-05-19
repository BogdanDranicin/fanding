package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
)

func ptr(f float64) *float64 { return &f }

func TestFormatAlert_withRates(t *testing.T) {
	snap := funding.FundingSnapshot{
		USDRUBF: funding.InstrumentFunding{
			OfficialRate: ptr(88.5),
			CBFunding:    ptr(0.12345),
		},
		EURRUBF: funding.InstrumentFunding{
			OfficialRate: ptr(96.2),
			CBFunding:    ptr(0.23456),
		},
		CNYRUBF: funding.InstrumentFunding{
			MOEXFunding: ptr(-0.01),
		},
	}

	ts := time.Date(2026, 5, 19, 11, 30, 0, 0, time.UTC)
	text := formatAlert(ts, snap)

	if !strings.Contains(text, "2026-05-19") {
		t.Error("missing date")
	}
	if !strings.Contains(text, "88.5000") {
		t.Error("missing USD rate")
	}
	if !strings.Contains(text, "USDRUBF") {
		t.Error("missing USDRUBF funding")
	}
	if !strings.Contains(text, "CNYRUBF") {
		t.Error("missing CNYRUBF funding")
	}
}

func TestFormatAlert_noRates(t *testing.T) {
	snap := funding.FundingSnapshot{}
	text := formatAlert(time.Now(), snap)
	if !strings.Contains(text, "НОВЫЕ ДАННЫЕ") {
		t.Error("missing header")
	}
	if strings.Contains(text, "Межбанк") {
		t.Error("should not include Межбанк when rates are nil")
	}
	if strings.Contains(text, "Фандинги") {
		t.Error("should not include Фандинги when fundings are nil")
	}
}
