package main

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source/moexiss"
)

// settlBackstop — последняя линия обороны ноги фьючерса: если к вечеру окно
// 10:00–15:30 не закрыл ни один потоковый источник, лента сделок за день
// перечитывается целиком и нога считается по ней.
//
// Зачем понадобилось (09.09.2026). Нога считается по двум потокам: живому потоку
// брокера и инкрементальному опросу ленты MOEX ISS. У обоих одна и та же слабость —
// они накапливают состояние. Живой поток теряет день, если подписка рвалась внутри
// окна; опрос ISS ведёт курсор TRADENO и после сбоя догоняет окно только вместе с
// перезапуском процесса. Когда не сработал ни один, движок морозил ногу
// приближением по приросту VOLTODAY, а оно врёт: в тот день EURRUBF встал на
// 99.41258 против настоящих 99.50425 по ленте, и подписчикам ушёл фандинг
// +0.06208 при биржевом SWAPRATE 0.15075 — больше чем вдвое мимо.
//
// Разовое чтение ленты от TRADENO=0 состояния не имеет вовсе: это те же сделки,
// что лежат на ISS, и запросить их можно в любой момент. Отсюда и правило —
// перечитывать, пока нога не станет точной, но не чаще, чем раз в settlRetry, и
// только в вечернем окне, когда лента заведомо довезла хвост.
//
// Сверено с биржей на том самом дне, 09.09.2026 (курс ЦБ на 10.09: USD 85.46,
// EUR 99.25; PREVSETTLEPRICE 86.47 и 100.50):
//
//	USDRUBF  нога 85.44726 (192575 лотов) → d = −0.01274, мёртвая зона → 0.00000
//	EURRUBF  нога 99.50425 ( 19142 лота ) → d =  0.25425, кап l2      → 0.15075
//
// то есть ровно то, что биржа опубликовала в SWAPRATE, тик в тик по обоим.
type settlBackstop struct {
	client  *moexiss.Client
	engine  *funding.Engine
	symbols []string
	log     zerolog.Logger
}

var mskZone = time.FixedZone("MSK", 3*60*60)

// settlRetry — как часто повторяется попытка. Чтение ленты USDRUBF за день — это
// десяток страниц по пять тысяч сделок, и молотить им ISS каждую минуту незачем:
// курс ЦБ публикуется не раньше 16:30, запас до рассылки — часы.
const settlRetry = 5 * time.Minute

// settlBackstopUntil — до какого часа МСК имеет смысл добирать ногу. После
// вечернего клиринга сегодняшний фандинг уже начислен и никому не нужен.
const settlBackstopUntil = 21

func newSettlBackstop(
	client *moexiss.Client, eng *funding.Engine, symbols []string, log zerolog.Logger,
) *settlBackstop {
	return &settlBackstop{
		client:  client,
		engine:  eng,
		symbols: symbols,
		log:     log.With().Str("component", "settl-backstop").Logger(),
	}
}

// Run крутится до отмены контекста. Первый обход — сразу: сервис могли поднять
// уже после клиринга, и тогда ждать пять минут не за чем.
func (b *settlBackstop) Run(ctx context.Context) {
	ticker := time.NewTicker(settlRetry)
	defer ticker.Stop()
	for {
		b.sweep(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// sweep обходит символы и добирает те, у которых ноги нет или она аварийная.
func (b *settlBackstop) sweep(ctx context.Context, now time.Time) {
	if h := now.In(mskZone).Hour(); h >= settlBackstopUntil {
		return
	}
	for _, sym := range b.symbols {
		mskDate, need := b.engine.SettlNeedsRebuild(sym, now)
		if !need {
			continue
		}
		if err := b.rebuild(ctx, sym, mskDate); err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.Warn().Err(err).Str("sym", sym).Str("date", mskDate).
				Msg("ногу фьючерса добрать не удалось, попробую снова")
		}
	}
}

// rebuild читает ленту сделок за день и ставит ногу.
func (b *settlBackstop) rebuild(ctx context.Context, sym, mskDate string) error {
	trades, err := b.client.FetchTradesSince(ctx, "futures", "forts", sym, 0)
	if err != nil && len(trades) == 0 {
		return err
	}
	leg, ok := windowVWAP(trades, mskDate)
	if !ok {
		return fmt.Errorf("лента за %s не закрывает окно: сделок в окне %d, за 15:30 %d",
			mskDate, leg.prints, leg.post)
	}
	if !b.engine.SetSettlFromTape(sym, mskDate, leg.vwap) {
		return nil
	}
	b.log.Warn().
		Str("sym", sym).
		Str("date", mskDate).
		Float64("settl_vwap", leg.vwap).
		Float64("window_lots", leg.volume).
		Int("window_trades", leg.prints).
		Int("post_1530_trades", leg.post).
		Msg("нога фьючерса добрана перечитанной лентой ISS")
	return nil
}

// tapeLeg — нога, собранная из ленты за день, и то, чем она подтверждается.
type tapeLeg struct {
	vwap   float64
	volume float64
	prints int
	post   int // сделок со временем ≥15:30 — доказательство, что окно кончилось
}

// windowVWAP считает средневзвешенную цену безадресных сделок за 10:00–15:30 МСК
// того же торгового дня. Отбор ровно тот же, что у аккумуляторов движка: адресные
// сделки идут мимо стакана и в биржевой VWAP не входят, а сделки чужого
// календарного дня биржа приписывает к сегодняшней сессии по объёму, но их цена
// относится к другому дню.
//
// Второе возвращаемое значение — годится ли нога. Годится она только тогда, когда
// в ленте есть хоть одна сделка ЗА границей окна: пока такой нет, лента может
// быть обрезана ровно там, где её оборвали, и отличить полное окно от неполного
// нечем. Это то же доказательство, по которому морозится нога из живого потока.
func windowVWAP(trades []moexiss.Trade, mskDate string) (tapeLeg, bool) {
	var leg tapeLeg
	var sumPV float64
	for _, tr := range trades {
		if tr.OffMarket || tr.Backdated || tr.Price <= 0 || tr.Quantity <= 0 {
			continue
		}
		t := tr.Timestamp.In(mskZone)
		if t.Format("2006-01-02") != mskDate {
			continue
		}
		h, m, _ := t.Clock()
		switch {
		case h < 10:
		case h > 15 || (h == 15 && m >= 30):
			leg.post++
		default:
			sumPV += tr.Price * tr.Quantity
			leg.volume += tr.Quantity
			leg.prints++
		}
	}
	if leg.volume <= 0 || leg.post == 0 {
		return leg, false
	}
	leg.vwap = sumPV / leg.volume
	return leg, true
}
