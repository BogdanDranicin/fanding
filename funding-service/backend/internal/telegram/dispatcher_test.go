package telegram

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rs/zerolog"

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
		// Только числовое значение ставки, 5 знаков — как публикует биржа (SWAPRATE).
		// Индикатор по-прежнему выбирается по проценту от нового курса ЦБ.
		"🔴USDRUBF: -0.11693",
		"🟢EURRUBF: +0.13337",
		"Курс ЦБ на 17.07.2026: USD 78.32 / EUR 89.33 / CNY 11.51",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// Процент из строк фандинга убран (27.07).
	if strings.Contains(text, "%") {
		t.Errorf("funding lines must carry no percentage, got:\n%s", text)
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

// fakeSender имитирует поход в Telegram: каждая отправка занимает delay и
// запоминается вместе с пиком одновременных запросов.
type fakeSender struct {
	delay time.Duration

	mu   sync.Mutex
	sent []int64

	inflight, peak atomic.Int64
}

func (f *fakeSender) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	n := f.inflight.Add(1)
	for {
		peak := f.peak.Load()
		if n <= peak || f.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer f.inflight.Add(-1)

	time.Sleep(f.delay)

	msg := c.(tgbotapi.MessageConfig)
	f.mu.Lock()
	f.sent = append(f.sent, msg.ChatID)
	f.mu.Unlock()
	return tgbotapi.Message{}, nil
}

// Рассылка идёт параллельно: последовательная отправка восьми сообщений по 100 мс
// с паузой 40 мс между ними заняла бы больше секунды, а подписчик №8 столько же и
// ждал бы. Проверяем, что запросы летят внахлёст и доходят до всех.
func TestBroadcast_sendsInParallel(t *testing.T) {
	fs := &fakeSender{delay: 100 * time.Millisecond}
	d := &Dispatcher{api: fs, log: zerolog.Nop()}

	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	start := time.Now()
	d.broadcast(context.Background(), "текст", ids, start, 0)
	elapsed := time.Since(start)

	fs.mu.Lock()
	got := len(fs.sent)
	fs.mu.Unlock()
	if got != len(ids) {
		t.Errorf("доставлено %d сообщений, ожидалось %d", got, len(ids))
	}
	if peak := fs.peak.Load(); peak < 2 {
		t.Errorf("пик одновременных отправок %d — рассылка осталась последовательной", peak)
	}
	// Последовательный вариант: 8 × (100 мс + 40 мс) ≈ 1.12 с.
	if elapsed > 700*time.Millisecond {
		t.Errorf("рассылка заняла %v — похоже на последовательную отправку", elapsed)
	}
}

// Отмена контекста прекращает рассылку и не оставляет висящих горутин-воркеров
// (иначе broadcast зависнет на wg.Wait и тест не завершится).
func TestBroadcast_stopsOnContextCancel(t *testing.T) {
	fs := &fakeSender{delay: 10 * time.Millisecond}
	d := &Dispatcher{api: fs, log: zerolog.Nop()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		d.broadcast(ctx, "текст", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, time.Now(), 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast не завершился после отмены контекста")
	}
}

// Служебного «Обновление сервиса / Сервис перезапущен» больше нет (06.08.2026):
// клиринг вообще не повод писать подписчику, а Run теперь принимает только канал
// публикаций ЦБ — уведомление физически неоткуда взять. Раньше сообщение уходило
// каждый день: сначала из-за окна по настенным часам, потом из-за флага Restored.
