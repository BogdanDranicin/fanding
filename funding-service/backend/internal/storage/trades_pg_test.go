//go:build pgtest

package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/funding-service/backend/internal/trades"
)

// Проверка SQL сделок на живой базе: PGTEST_DSN=postgres://... go test -tags pgtest ./internal/storage/
func TestTradesOnPostgres(t *testing.T) {
	dsn := os.Getenv("PGTEST_DSN")
	if dsn == "" {
		t.Skip("PGTEST_DSN не задан")
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	s := NewStore(pool)

	at := time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)
	send := func(msgID int64, text string, reply *int64) []TradeEvent {
		t.Helper()
		at = at.Add(time.Minute)
		ev, dup, err := s.IngestTradeMessage(ctx, TradeMessageIn{
			ChannelID: -1003229564526, ChannelTitle: "Profit King [Ded]", MsgID: msgID,
			PostedAt: at, Text: text, ReplyTo: reply,
		})
		if err != nil {
			t.Fatalf("ingest %d: %v", msgID, err)
		}
		if dup {
			t.Fatalf("ingest %d: неожиданный дубль", msgID)
		}
		return ev
	}

	ev := send(1, "Роснефть покупка 25% Модельный портфель 1 360,65", nil)
	if len(ev) != 1 || ev[0].Action != "open" || ev[0].Ticker != "ROSN" || ev[0].Size != "25%" {
		t.Fatalf("открытие: %+v", ev)
	}
	send(2, "Роснефть покупка 75% Модельный порфтель 1 362,4 Стоп на всю позицию под минимум", nil)
	open, err := s.ListTradePositions(ctx, "open", 100)
	if err != nil || len(open) != 1 || open[0].Size != "100%" || open[0].Stop == "" || *open[0].EntryPrice != 360.65 {
		t.Fatalf("после добора: %+v err=%v", open, err)
	}

	if _, dup, err := s.IngestTradeMessage(ctx, TradeMessageIn{
		ChannelID: -1003229564526, ChannelTitle: "Profit King [Ded]", MsgID: 2, PostedAt: at, Text: "Роснефть покупка 75%",
	}); err != nil || !dup {
		t.Fatalf("повтор сообщения должен узнаваться как дубль: dup=%v err=%v", dup, err)
	}

	send(3, "Газпром покупка 100% 97,35", nil)
	send(4, "Модельный портфель 1 Закрыл Роснефть остаток 🎖+3%", nil)
	one := int64(3)
	ev = send(5, "Закрыл", &one)
	if len(ev) != 1 || ev[0].Ticker != "GAZP" || ev[0].Action != "close" {
		t.Fatalf("ответ «Закрыл» на сообщение о Газпроме: %+v", ev)
	}
	open, _ = s.ListTradePositions(ctx, "open", 100)
	closed, _ := s.ListTradePositions(ctx, "closed", 100)
	if len(open) != 0 || len(closed) != 2 {
		t.Fatalf("ждали 0 открытых и 2 закрытых: %d / %d", len(open), len(closed))
	}

	ev = send(6, "Открыл шорт и сразу добавил: Сбербанк шорт 280, стоп 285", nil)
	if len(ev) == 0 {
		t.Fatal("сделка без глагола с направлением, ценой и стопом должна открыться")
	}
	send(7, "Сбербанк покупка 50% 279", nil)
	open, _ = s.ListTradePositions(ctx, "open", 100)
	if len(open) != 1 || open[0].Direction != trades.DirLong {
		t.Fatalf("встречная покупка переворачивает шорт в лонг: %+v", open)
	}

	events, err := s.ListTradeEvents(ctx, 0, 100)
	if err != nil || len(events) < 6 || events[0].Message == "" {
		t.Fatalf("лента: %d событий, err=%v", len(events), err)
	}
	after, err := s.ListTradeEvents(ctx, events[2].ID, 100)
	if err != nil || len(after) != 2 || after[0].ID != events[1].ID {
		t.Fatalf("события после id: %+v err=%v", after, err)
	}
	last, _ := s.LastTradeEventID(ctx)
	if last != events[0].ID {
		t.Fatalf("последний id %d != %d", last, events[0].ID)
	}

	p := open[0]
	p.Size = "70%"
	p.Note = "поправил руками"
	saved, err := s.SaveTradePosition(ctx, p)
	if err != nil || !saved.Manual || saved.Size != "70%" || saved.Note != "поправил руками" {
		t.Fatalf("ручная правка: %+v err=%v", saved, err)
	}
	p.Status = trades.StatusClosed
	saved, err = s.SaveTradePosition(ctx, p)
	if err != nil || saved.ClosedAt == nil {
		t.Fatalf("ручное закрытие ставит время: %+v err=%v", saved, err)
	}
	created, err := s.SaveTradePosition(ctx, trades.Position{
		Author: "Вадик [vadya93]", Ticker: "SMLT", Direction: trades.DirShort, Size: "4000 шт", Status: trades.StatusOpen,
	})
	if err != nil || created.ID == 0 || !created.Manual || created.OpenedAt.IsZero() {
		t.Fatalf("ручное создание: %+v err=%v", created, err)
	}
	if err := s.DeleteTradePosition(ctx, created.ID); err != nil {
		t.Fatalf("удаление: %v", err)
	}
	if err := s.DeleteTradePosition(ctx, created.ID); err != ErrTradeNotFound {
		t.Fatalf("повторное удаление: %v", err)
	}
	if _, err := s.SaveTradePosition(ctx, trades.Position{ID: 999999, Author: "x", Ticker: "y",
		Direction: trades.DirLong, Status: trades.StatusOpen}); err != ErrTradeNotFound {
		t.Fatalf("правка несуществующей: %v", err)
	}
}
