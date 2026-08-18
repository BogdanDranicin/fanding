package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/funding-service/backend/internal/robots"
	"github.com/funding-service/backend/internal/source/tinvest"
)

// tinvestSource связывает поток обезличенных сделок брокера с поиском роботов.
//
// Живёт здесь, а не в internal/source/tinvest: пакет источника не должен знать про
// детектор, а детектор — про конкретного брокера. Стыковка — дело сборки сервиса.
type tinvestSource struct {
	client *tinvest.Client

	mu    sync.Mutex
	byUID map[string]string // uid -> тикер биржи
	byTic map[string]string // тикер -> uid
}

func newTInvestSource(c *tinvest.Client) *tinvestSource {
	return &tinvestSource{client: c}
}

// Symbols тянет каталог инструментов и запоминает соответствие тикеров и UID:
// подписка в стриме идёт по UID, а весь сервис оперирует тикерами биржи.
func (s *tinvestSource) Symbols(ctx context.Context) ([]robots.StreamInstrument, error) {
	instruments, err := s.client.Instruments(ctx)
	if err != nil {
		return nil, err
	}

	byUID := make(map[string]string, len(instruments))
	byTic := make(map[string]string, len(instruments))
	out := make([]robots.StreamInstrument, 0, len(instruments))
	for _, in := range instruments {
		if in.Ticker == "" || in.UID == "" {
			continue
		}
		// Тикер у брокера совпадает с SECID биржи и для акций, и для срочных
		// контрактов (MIX-9.26 — это MXU6 в обоих местах), так что перевод не нужен.
		if _, dup := byTic[in.Ticker]; dup {
			continue
		}
		byUID[in.UID] = in.Ticker
		byTic[in.Ticker] = in.UID
		out = append(out, robots.StreamInstrument{Symbol: in.Ticker, Board: in.ClassCode})
	}

	s.mu.Lock()
	s.byUID, s.byTic = byUID, byTic
	s.mu.Unlock()
	return out, nil
}

// Run поднимает столько стримов, сколько нужно под лимит подписок, и переводит
// сделки в принты детектора. Возвращает управление, когда оборвался любой из них:
// коллектор переподключит всё разом, а лента тем временем идёт из ISS.
func (s *tinvestSource) Run(ctx context.Context, symbols []string, out chan<- robots.Print) error {
	s.mu.Lock()
	byUID, byTic := s.byUID, s.byTic
	s.mu.Unlock()
	if byTic == nil {
		return fmt.Errorf("tinvest: каталог инструментов не загружен")
	}

	uids := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		if uid, ok := byTic[sym]; ok {
			uids = append(uids, uid)
		}
	}
	if len(uids) == 0 {
		return fmt.Errorf("tinvest: ни один инструмент не найден в каталоге")
	}

	chunks := tinvest.ChunkUIDs(uids)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	trades := make(chan tinvest.Trade, cap(out))
	errs := make(chan error, len(chunks))
	var wg sync.WaitGroup
	for _, chunk := range chunks {
		wg.Add(1)
		go func(uids []string) {
			defer wg.Done()
			errs <- s.client.StreamTrades(ctx, uids, byUID, trades)
		}(chunk)
	}
	go func() {
		wg.Wait()
		close(trades)
	}()

	for {
		select {
		case tr, ok := <-trades:
			if !ok {
				return <-errs
			}
			select {
			case out <- printOf(tr):
			case <-ctx.Done():
				return ctx.Err()
			}
		case err := <-errs:
			// Один оборвавшийся стрим валит всю пачку: держать половину подписок
			// живой смысла нет, переподключаемся целиком.
			cancel()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// printOf переводит сделку брокера в принт детектора.
func printOf(t tinvest.Trade) robots.Print {
	side := robots.SideSell
	if t.Buy {
		side = robots.SideBuy
	}
	return robots.Print{
		Symbol: strings.ToUpper(t.Ticker),
		// Время биржевое и, в отличие от ленты ISS, точнее секунды.
		Time:  t.Time,
		Price: t.Price,
		Qty:   t.Qty,
		Side:  side,
	}
}
