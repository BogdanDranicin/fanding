package main

import (
	"context"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
)

func ptr(v float64) *float64 { return &v }

// Строка уведомления — это то немногое, что пользователь увидит на экране
// телефона: ставки в том же виде, в каком их публикует биржа.
func TestFundingLine(t *testing.T) {
	snap := funding.FundingSnapshot{}
	snap.USDRUBF.CBFunding = ptr(0.11730)
	snap.EURRUBF.CBFunding = ptr(-0.11693)

	if got := fundingLine(snap); got != "USD +0.11730 · EUR -0.11693" {
		t.Errorf("строка уведомления %q", got)
	}
}

// Публикация, по которой фандинг ещё не посчитан, уведомления не даёт: пустое
// «Фандинг зафиксирован» без единой цифры уже приходило (12.08.2026) и
// сообщало ровно ничего.
func TestFundingLineEmpty(t *testing.T) {
	if got := fundingLine(funding.FundingSnapshot{}); got != "" {
		t.Errorf("без фандинга ждали пустую строку, получили %q", got)
	}
}

// Сигнал о публикации летит параллельно тикам с новыми курсами, поэтому первый
// же снапшот обычно ещё без пересчитанного фандинга — его надо дождаться.
func TestAwaitPushFundingWaitsForRecalc(t *testing.T) {
	calls := 0
	snapshotFn := func() funding.FundingSnapshot {
		calls++
		var s funding.FundingSnapshot
		if calls > 3 {
			s.USDRUBF.CBFunding = ptr(0.1)
		}
		return s
	}

	snap := awaitPushFunding(context.Background(), snapshotFn)
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("дождались снапшота без фандинга")
	}
	if calls < 4 {
		t.Errorf("движок опрошен %d раз — ожидание не работает", calls)
	}
}

// Отменённый контекст не должен держать горутину до конца таймаута.
func TestAwaitPushFundingStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		awaitPushFunding(ctx, func() funding.FundingSnapshot { return funding.FundingSnapshot{} })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ожидание не прервалось по отмене контекста")
	}
}
