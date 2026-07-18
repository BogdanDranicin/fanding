package telegram

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source/cbr"
)

func ptr(f float64) *float64 { return &f }

func TestFormatCBRAlert_withRates(t *testing.T) {
	info := cbr.PublicationInfo{
		Date: "17.07.2026",
		USD:  78.3181,
		EUR:  89.3296,
		CNY:  11.5139,
	}
	snap := funding.FundingSnapshot{
		USDRUBF: funding.InstrumentFunding{
			OfficialRate: ptr(78.3181),
			CBFunding:    ptr(-0.116935),
		},
		EURRUBF: funding.InstrumentFunding{
			OfficialRate: ptr(89.3296),
			CBFunding:    ptr(0.133365),
		},
		CNYRUBF: funding.InstrumentFunding{
			MOEXFunding: ptr(0.0069),
		},
	}

	ts := time.Date(2026, 7, 16, 14, 56, 31, 0, time.UTC) // 17:56:31 МСК
	text := formatCBRAlert(ts, info, snap)

	for _, want := range []string{
		"Фандинг зафиксирован",
		"17:56:31",
		"🔴USDRUBF: -0.149% (-0.1169)",
		"🟢EURRUBF: +0.149% (+0.1334)",
		"Курс ЦБ на 17.07.2026: USD 78.32 / EUR 89.33 / CNY 11.51",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// CNY фандинг убран из уведомлений (18.07) — строки индикатора по юаню быть не должно.
	if strings.Contains(text, "CNYRUBF") {
		t.Errorf("CNY funding line must be gone, got:\n%s", text)
	}
}

// Фандинги ещё не пересчитаны (таймаут ожидания) — сообщение уходит без строк
// индикатора, но с правильными НОВЫМИ курсами из PublicationInfo.
func TestFormatCBRAlert_fundingNotReady(t *testing.T) {
	info := cbr.PublicationInfo{Date: "17.07.2026", USD: 78.3181, EUR: 89.3296}
	snap := funding.FundingSnapshot{
		USDRUBF: funding.InstrumentFunding{OfficialRate: ptr(77.9568)}, // движок ещё на вчерашнем
		EURRUBF: funding.InstrumentFunding{OfficialRate: ptr(88.9097)},
	}
	text := formatCBRAlert(time.Now(), info, snap)

	if !strings.Contains(text, "USD 78.32") || !strings.Contains(text, "EUR 89.33") {
		t.Errorf("must contain the NEW published rates, got:\n%s", text)
	}
	if strings.Contains(text, "77.96") || strings.Contains(text, "88.91") {
		t.Errorf("must not leak yesterday's rates from the snapshot, got:\n%s", text)
	}
	if strings.Contains(text, "USDRUBF") {
		t.Errorf("no funding lines expected when CBFunding is nil, got:\n%s", text)
	}
}

func TestIndicatorEmoji(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{0.150, "🟢"},
		{0.14, "🟢"},
		{0.10, "🟡"},
		{0.05, "🟡"},
		{0.0, "⚪️"},
		{-0.049, "⚪️"},
		{-0.05, "🟠"},
		{-0.139, "🟠"},
		{-0.14, "🔴"},
		{-0.2, "🔴"},
	}
	for _, c := range cases {
		if got := indicatorEmoji(c.pct); got != c.want {
			t.Errorf("indicatorEmoji(%v) = %s, want %s", c.pct, got, c.want)
		}
	}
}

// awaitCBFunding должен вернуться, как только движок догнал публикацию,
// не дожидаясь полного таймаута.
func TestAwaitCBFunding_returnsWhenEngineCatchesUp(t *testing.T) {
	info := cbr.PublicationInfo{USD: 78.3181, EUR: 89.3296}

	var calls atomic.Int64
	d := &Dispatcher{
		snapshotFn: func() funding.FundingSnapshot {
			if calls.Add(1) < 3 { // первые снапшоты — вчерашние курсы, без фандинга
				return funding.FundingSnapshot{
					USDRUBF: funding.InstrumentFunding{OfficialRate: ptr(77.9568)},
					EURRUBF: funding.InstrumentFunding{OfficialRate: ptr(88.9097)},
				}
			}
			return funding.FundingSnapshot{
				USDRUBF: funding.InstrumentFunding{OfficialRate: ptr(78.3181), CBFunding: ptr(-0.1169)},
				EURRUBF: funding.InstrumentFunding{OfficialRate: ptr(89.3296), CBFunding: ptr(0.1334)},
			}
		},
	}

	start := time.Now()
	snap := d.awaitCBFunding(context.Background(), info)
	if snap.USDRUBF.CBFunding == nil || snap.EURRUBF.CBFunding == nil {
		t.Fatal("expected the caught-up snapshot with CBFunding set")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v — should return well before the 10s timeout", elapsed)
	}
}

func TestIsSettlementTime(t *testing.T) {
	cases := []struct {
		utc  time.Time
		want bool
	}{
		{time.Date(2026, 7, 16, 12, 30, 5, 0, time.UTC), true},   // 15:30:05 МСК — настоящий клиринг
		{time.Date(2026, 7, 16, 12, 44, 59, 0, time.UTC), true},  // 15:44:59 МСК — край окна
		{time.Date(2026, 7, 16, 12, 45, 0, 0, time.UTC), false},  // 15:45:00 МСК — уже вне
		{time.Date(2026, 7, 16, 20, 34, 39, 0, time.UTC), false}, // 23:34:39 МСК — рестарт докера
		{time.Date(2026, 7, 16, 7, 0, 0, 0, time.UTC), false},    // 10:00 МСК
	}
	for _, c := range cases {
		if got := isSettlementTime(c.utc); got != c.want {
			t.Errorf("isSettlementTime(%s МСК) = %v, want %v",
				c.utc.In(time.FixedZone("MSK", 3*60*60)).Format("15:04:05"), got, c.want)
		}
	}
}

func TestFormatRestartNotice(t *testing.T) {
	ts := time.Date(2026, 7, 16, 20, 34, 39, 0, time.UTC) // 23:34:39 МСК
	text := formatRestartNotice(ts)
	if !strings.Contains(text, "Обновление сервиса") {
		t.Error("missing header")
	}
	if !strings.Contains(text, "23:34:39") {
		t.Error("missing MSK time")
	}
	if strings.Contains(text, "зафиксирован") || strings.Contains(text, "Курс ЦБ") {
		t.Error("restart notice must not look like a funding alert")
	}
}
