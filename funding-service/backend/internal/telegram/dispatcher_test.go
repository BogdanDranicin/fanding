package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
)

func ptr(f float64) *float64 { return &f }

func TestFormatCBRAlert_withRates(t *testing.T) {
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
	text := formatCBRAlert(ts, snap)

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

func TestFormatCBRAlert_noRates(t *testing.T) {
	snap := funding.FundingSnapshot{}
	text := formatCBRAlert(time.Now(), snap)
	if !strings.Contains(text, "Новые курсы ЦБ опубликованы") {
		t.Error("missing header")
	}
	if strings.Contains(text, "Курсы ЦБ") {
		t.Error("should not include rates section when rates are nil")
	}
	if strings.Contains(text, "Точные фандинги") {
		t.Error("should not include funding section when fundings are nil")
	}
}

func TestFormatSettlAlert_withPredicted(t *testing.T) {
	snap := funding.FundingSnapshot{
		USDRUBF: funding.InstrumentFunding{
			PredictedFunding: ptr(0.00045),
			OfficialRate:     ptr(88.5),
		},
		EURRUBF: funding.InstrumentFunding{
			PredictedFunding: ptr(-0.00012),
			OfficialRate:     ptr(96.2),
		},
	}

	ts := time.Date(2026, 5, 19, 12, 30, 0, 0, time.UTC) // 15:30 MSK
	text := formatSettlAlert(ts, snap)

	if !strings.Contains(text, "Прогнозный фандинг зафиксирован") {
		t.Error("missing header")
	}
	if !strings.Contains(text, "USDRUBF") {
		t.Error("missing USDRUBF")
	}
	if !strings.Contains(text, "15:30:00") {
		t.Error("missing time")
	}
}
