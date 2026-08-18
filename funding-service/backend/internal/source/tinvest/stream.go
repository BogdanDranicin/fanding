package tinvest

import (
	"context"
	"fmt"
	"time"

	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
)

// maxSubscriptionsPerStream — предел API: столько подписок помещается в одно
// stream-соединение сервиса котировок, суммарно по свечам, стаканам и сделкам.
// Инструментов у нас больше, поэтому подписки режутся на пачки, каждая в своём
// соединении. Лимит на число соединений заметно выше (у тарифа investor — 32).
const maxSubscriptionsPerStream = 300

// StreamTrades ведёт подписку на ленту обезличенных сделок по указанным
// инструментам и складывает сделки в out, пока не отменят контекст.
//
// Возврат означает обрыв: вызывающий решает, переподключаться или переходить на
// запасной источник. Само переподключение здесь не делается намеренно — политика
// восстановления живёт там же, где знание о запасном источнике.
func (c *Client) StreamTrades(ctx context.Context, uids []string, byUID map[string]string, out chan<- Trade) error {
	if len(uids) == 0 {
		return fmt.Errorf("tinvest: пустой список инструментов")
	}
	if len(uids) > maxSubscriptionsPerStream {
		return fmt.Errorf("tinvest: %d подписок на одно соединение, предел %d",
			len(uids), maxSubscriptionsPerStream)
	}

	stream, err := c.market.MarketDataStream(ctx)
	if err != nil {
		return fmt.Errorf("tinvest: открытие стрима: %w", err)
	}

	instruments := make([]*pb.TradeInstrument, 0, len(uids))
	for _, uid := range uids {
		instruments = append(instruments, &pb.TradeInstrument{InstrumentId: uid})
	}
	req := &pb.MarketDataRequest{
		Payload: &pb.MarketDataRequest_SubscribeTradesRequest{
			SubscribeTradesRequest: &pb.SubscribeTradesRequest{
				SubscriptionAction: pb.SubscriptionAction_SUBSCRIPTION_ACTION_SUBSCRIBE,
				Instruments:        instruments,
				// Только биржевые сделки: дилерские идут мимо стакана и к роботам
				// в биржевой ленте отношения не имеют — тот же смысл, что у отсева
				// адресных сделок в ленте ISS.
				TradeSource: pb.TradeSourceType_TRADE_SOURCE_EXCHANGE,
			},
		},
	}
	if err := stream.Send(req); err != nil {
		return fmt.Errorf("tinvest: подписка: %w", err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("tinvest: чтение стрима: %w", err)
		}
		trade := resp.GetTrade()
		if trade == nil {
			// В том же потоке приходят подтверждения подписки и пинги — они нам
			// не нужны, но их приход означает, что соединение живо.
			continue
		}
		ticker, ok := byUID[trade.GetInstrumentUid()]
		if !ok {
			continue
		}
		select {
		case out <- Trade{
			Ticker: ticker,
			Time:   trade.GetTime().AsTime(),
			Price:  quotationToFloat(trade.GetPrice()),
			Qty:    float64(trade.GetQuantity()),
			Buy:    trade.GetDirection() == pb.TradeDirection_TRADE_DIRECTION_BUY,
		}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ChunkUIDs режет список инструментов на пачки под предел подписок одного стрима.
func ChunkUIDs(uids []string) [][]string {
	var out [][]string
	for len(uids) > maxSubscriptionsPerStream {
		out = append(out, uids[:maxSubscriptionsPerStream])
		uids = uids[maxSubscriptionsPerStream:]
	}
	if len(uids) > 0 {
		out = append(out, uids)
	}
	return out
}

// ReconnectDelay — пауза перед повторной попыткой после обрыва стрима. Растёт до
// потолка, чтобы при недоступном API не молотить в него без остановки.
func ReconnectDelay(attempt int) time.Duration {
	const (
		base = 2 * time.Second
		max  = time.Minute
	)
	d := base << uint(min(attempt, 5))
	if d > max {
		return max
	}
	return d
}
