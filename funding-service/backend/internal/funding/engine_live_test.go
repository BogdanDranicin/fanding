package funding_test

import (
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

// Живой поток сделок брокера как источник ноги фьючерса (28.08.2026).
//
// До него нога считалась по публичной ленте MOEX ISS, которая отдаёт сделку
// через пятнадцать минут. Из-за этого в 15:30 движок не мог отличить «окно
// кончилось» от «лента отстала» и регулярно морозил ногу обрезанным окном:
// 19.08.2026 EUR ушёл подписчикам как 0.03367 вместо биржевых 0.02919.
//
// Проверки ниже описывают ровно ту границу, которая делает живую ногу
// пригодной: поток должен покрывать окно с самого открытия, не рваться внутри
// него и перешагнуть 15:30 своей же сделкой.

func liveTradeTick(sym string, price, qty float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Volume:    qty,
		Kind:      source.KindTrade,
		Timestamp: ts,
		Source:    "tinvest",
		Live:      true,
	}
}

func streamTick(sym string, kind source.TickKind, ts time.Time) source.Tick {
	return source.Tick{Symbol: sym, Kind: kind, Timestamp: ts, Source: "tinvest", Live: true}
}

// atToday — время сегодняшнего дня по Москве. Сценарии заморозки завязаны на
// «сегодня»: CBFunding появляется только по публикации, датированной текущим днём.
func atToday(h, m int) time.Time {
	settle := todaySettle()
	return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, settle.Location())
}

// Главная проверка: окно закрывается сделкой живого потока ровно в 15:30, и для
// этого не нужно ни одной сделки из ленты ISS. Именно этого и не хватало —
// раньше в этот момент у движка на руках были данные только до ~15:15.
func TestEngine_LiveStreamFreezesLegAtSettlement(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	// VWAP окна = (80×10 + 82×30) / 40 = 81.5
	e.Ingest(liveTradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(liveTradeTick(sym, 82.0, 30, atToday(12, 0)))

	if got := e.Snapshot().USDRUBF.SettlVWAP; got != nil {
		t.Fatalf("нога заморожена до 15:30: %.6f", *got)
	}

	// Первая сделка за 15:30 — окно кончилось. Сама в него не входит.
	e.Ingest(liveTradeTick(sym, 90.0, 5, atToday(15, 30)))

	snap := e.Snapshot().USDRUBF
	if snap.SettlVWAP == nil {
		t.Fatal("нога не заморожена сделкой живого потока за 15:30")
	}
	if diff := *snap.SettlVWAP - 81.5; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("нога: хочу 81.5, получил %.6f", *snap.SettlVWAP)
	}
	if snap.SettlProvisional {
		t.Error("нога живого потока не может быть предварительной: окно закрыто её же сделкой")
	}
	if snap.SettlSource != "live" {
		t.Errorf("источник ноги: хочу live, получил %q", snap.SettlSource)
	}
}

// Лента ISS, догнав окно через пятнадцать минут, подтверждает живую ногу и
// ничего не меняет. Это сверка двух независимых источников, а не переписывание.
func TestEngine_ISSConfirmsLiveLeg(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	e.Ingest(liveTradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(liveTradeTick(sym, 82.0, 30, atToday(12, 0)))
	e.Ingest(liveTradeTick(sym, 90.0, 5, atToday(15, 30)))

	// Те же сделки приезжают лентой ISS с опозданием.
	e.Ingest(tradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(tradeTick(sym, 82.0, 30, atToday(12, 0)))
	e.Ingest(tradeTick(sym, 90.0, 5, atToday(15, 30)))

	snap := e.Snapshot().USDRUBF
	if diff := *snap.SettlVWAP - 81.5; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("нога после сверки: хочу 81.5, получил %.6f", *snap.SettlVWAP)
	}
	if snap.SettlSource != "live" {
		t.Errorf("совпавшая сверка не должна менять источник, получил %q", snap.SettlSource)
	}
}

// Если живой поток всё-таки потерял сделки, догнавшая лента ISS переписывает
// ногу: у неё есть курсор TRADENO и она доигрывает пропущенное, у потока — нет.
func TestEngine_ISSOverridesLiveLegOnMismatch(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	// Поток видел только первую сделку окна.
	e.Ingest(liveTradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(liveTradeTick(sym, 90.0, 5, atToday(15, 30)))

	if got := *e.Snapshot().USDRUBF.SettlVWAP; got != 80.0 {
		t.Fatalf("нога живого потока: хочу 80, получил %.6f", got)
	}

	// Лента ISS знает обе сделки окна: VWAP = 81.5.
	e.Ingest(tradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(tradeTick(sym, 82.0, 30, atToday(12, 0)))
	e.Ingest(tradeTick(sym, 90.0, 5, atToday(15, 30)))

	snap := e.Snapshot().USDRUBF
	if diff := *snap.SettlVWAP - 81.5; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("нога после расхождения: хочу 81.5 (лента ISS), получил %.6f", *snap.SettlVWAP)
	}
	if snap.SettlSource != "iss-trades" {
		t.Errorf("источник ноги после расхождения: хочу iss-trades, получил %q", snap.SettlSource)
	}
}

// Обрыв подписки внутри окна лишает живую ногу права на заморозку: пропущенные
// за время обрыва сделки поток не досылает.
func TestEngine_LiveLegRejectedAfterGapInsideWindow(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	e.Ingest(liveTradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(streamTick(sym, source.KindStreamDown, atToday(11, 0)))
	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(11, 5)))
	e.Ingest(liveTradeTick(sym, 82.0, 30, atToday(12, 0)))
	e.Ingest(liveTradeTick(sym, 90.0, 5, atToday(15, 30)))

	if got := e.Snapshot().USDRUBF.SettlVWAP; got != nil {
		t.Fatalf("нога заморожена по потоку с дырой в окне: %.6f", *got)
	}

	// Лента ISS доигрывает всё окно и морозит ногу сама.
	e.Ingest(tradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(tradeTick(sym, 81.0, 20, atToday(11, 2)))
	e.Ingest(tradeTick(sym, 82.0, 30, atToday(12, 0)))
	e.Ingest(tradeTick(sym, 90.0, 5, atToday(15, 30)))

	snap := e.Snapshot().USDRUBF
	if snap.SettlVWAP == nil {
		t.Fatal("лента ISS обязана заморозить ногу вместо потока с дырой")
	}
	// (80×10 + 81×20 + 82×30) / 60 = 81.3333…
	if diff := *snap.SettlVWAP - 81.0 - 1.0/3.0; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("нога по ленте ISS: хочу 81.3333, получил %.6f", *snap.SettlVWAP)
	}
	if snap.SettlSource != "iss-trades" {
		t.Errorf("источник ноги: хочу iss-trades, получил %q", snap.SettlSource)
	}
}

// Поток, поднявшийся после открытия торгов, утро окна не видел — морозить по
// нему нельзя, каким бы свежим он ни был.
func TestEngine_LiveLegRejectedWhenStreamStartedInsideWindow(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(12, 0)))
	e.Ingest(liveTradeTick(sym, 82.0, 30, atToday(12, 1)))
	e.Ingest(liveTradeTick(sym, 90.0, 5, atToday(15, 30)))

	if got := e.Snapshot().USDRUBF.SettlVWAP; got != nil {
		t.Fatalf("нога заморожена по потоку, поднявшемуся среди окна: %.6f", *got)
	}
}

// Предварительную ногу (лента ISS не довезла окно к истечению отсрочки)
// переписывает живой поток, как только он перешагнёт 15:30.
func TestEngine_LiveRefinesProvisionalLeg(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	// Лента ISS обрывается на 15:11 — ровно сценарий 19.08.2026.
	e.Ingest(tradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(tradeTick(sym, 82.0, 30, atToday(15, 11)))
	// Истёкшая отсрочка: marketdata с рыночным временем 17:00 морозит тем, что есть.
	// VOLTODAY совпадает с собранным лентой объёмом — полнота фида сходится, и
	// движок берёт точную (но обрезанную) ленту, а не приближение по ΔVOLTODAY.
	e.Ingest(moexTick(sym, 83.0, 40, atToday(17, 0)))

	snap := e.Snapshot().USDRUBF
	if snap.SettlVWAP == nil || !snap.SettlProvisional {
		t.Fatalf("нога должна быть заморожена предварительно, получил %+v", snap.SettlVWAP)
	}

	// Живой поток закрывает окно целиком.
	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	e.Ingest(liveTradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(liveTradeTick(sym, 82.0, 30, atToday(15, 11)))
	e.Ingest(liveTradeTick(sym, 84.0, 20, atToday(15, 25)))
	e.Ingest(liveTradeTick(sym, 90.0, 5, atToday(15, 31)))

	snap = e.Snapshot().USDRUBF
	if snap.SettlProvisional {
		t.Error("уточнённая нога больше не предварительная")
	}
	// (80×10 + 82×30 + 84×20) / 60 = 82.1333…
	want := (80.0*10 + 82.0*30 + 84.0*20) / 60
	if diff := *snap.SettlVWAP - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("уточнённая нога: хочу %.6f, получил %.6f", want, *snap.SettlVWAP)
	}
	if snap.SettlSource != "live" {
		t.Errorf("источник уточнённой ноги: хочу live, получил %q", snap.SettlSource)
	}
}

// Последняя цена и прогнозный фандинг берутся из живого потока, а не из ленты
// ISS: на сайте они показывали рынок пятнадцатиминутной давности.
func TestEngine_LiveStreamDrivesLastPriceAndPrediction(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	// Лента ISS отстаёт: её последняя цена — 80.
	e.Ingest(moexTick(sym, 80.0, 1000, atToday(10, 0)))
	e.Ingest(moexTick(sym, 80.0, 1100, atToday(10, 1)))
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 80.0, 0, atToday(10, 1)))

	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	e.Ingest(liveTradeTick(sym, 85.0, 100, atToday(10, 15)))

	snap := e.Snapshot().USDRUBF
	if snap.LastPrice != 85.0 {
		t.Errorf("последняя цена: хочу 85 (живой поток), получил %.4f", snap.LastPrice)
	}
	if snap.PredictedFunding == nil {
		t.Fatal("прогнозный фандинг должен считаться по живой ноге")
	}
	// Нога фьючерса 85, прогноз курса ЦБ 80 → d = 5, всё упирается в кап 0.0015×80.
	if want := 0.0015 * 80.0; *snap.PredictedFunding != want {
		t.Errorf("прогнозный фандинг: хочу %.6f, получил %.6f", want, *snap.PredictedFunding)
	}
}

// Живая нога не морозится, пока поток не перешагнул 15:30 своей же сделкой:
// сама по себе стрелка часов ничего не доказывает.
func TestEngine_LiveLegWaitsForItsOwnPost1530Trade(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	e.Ingest(liveTradeTick(sym, 80.0, 10, atToday(10, 0)))
	e.Ingest(liveTradeTick(sym, 82.0, 30, atToday(15, 29)))

	if got := e.Snapshot().USDRUBF.SettlVWAP; got != nil {
		t.Fatalf("нога заморожена до сделки за 15:30: %.6f", *got)
	}
}

// Нога, замороженная живым потоком, доводится до фандинга по публикации ЦБ —
// без пометки «предварительно», то есть без подпорки в уведомлении бота.
func TestEngine_LiveLegYieldsFinalCBFunding(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	e.Ingest(prevSettleTick(sym, 81.44, atToday(9, 0)))
	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(9, 30)))
	e.Ingest(liveTradeTick(sym, 81.60, 100, atToday(10, 0)))
	e.Ingest(liveTradeTick(sym, 81.60, 100, atToday(15, 0)))
	e.Ingest(liveTradeTick(sym, 90.0, 5, atToday(15, 30)))

	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 81.44, atToday(16, 35)))

	snap := e.Snapshot().USDRUBF
	if snap.CBFunding == nil {
		t.Fatal("CBFunding должен появиться после публикации ЦБ")
	}
	if snap.SettlProvisional {
		t.Error("нога живого потока уходит в бот без пометки «уточняется»")
	}
	// d = 81.60 − 81.44 = 0.16; l1 = 0.001×81.44 = 0.08144; l2 = 0.0015×81.44 = 0.12216.
	// clamp: 0.16 − 0.08144 = 0.07856, внутри капа.
	want := 0.16 - 0.001*81.44
	if diff := *snap.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: хочу %.6f, получил %.6f", want, *snap.CBFunding)
	}
}

// Поток, поднявшийся среди дня, не должен подменять собой прогнозную ногу:
// у него на руках огрызок сессии, а прогноз считается по окну с 10:00. Лента
// ISS в этом случае честнее — она доигрывает день с начала.
func TestEngine_MiddayStreamDoesNotHijackPrediction(t *testing.T) {
	e := funding.NewEngine()
	sym := source.SymbolUSDRUBF

	// Лента ISS видела всё окно: VWAP = (80×100 + 82×100) / 200 = 81.
	e.Ingest(tradeTick(sym, 80.0, 100, atToday(10, 0)))
	e.Ingest(tradeTick(sym, 82.0, 100, atToday(11, 0)))
	e.Ingest(moexTick(sym, 82.0, 200, atToday(11, 1))) // VOLTODAY сходится с лентой
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 81.0, 0, atToday(11, 1)))

	// Сервис перезапустился в полдень, поток поднялся только сейчас.
	e.Ingest(streamTick(sym, source.KindStreamUp, atToday(12, 0)))
	e.Ingest(liveTradeTick(sym, 90.0, 10, atToday(12, 1)))

	snap := e.Snapshot().USDRUBF
	if snap.PredictedFunding == nil {
		t.Fatal("прогнозный фандинг должен считаться по ленте ISS")
	}
	// Нога по ленте 81 против прогноза курса 81 → d = 0, мёртвая зона → 0.
	// Если бы победил живой огрызок (90), d = 9 упёрлось бы в кап.
	if *snap.PredictedFunding != 0 {
		t.Errorf("прогноз посчитан по огрызку живого потока: %.6f", *snap.PredictedFunding)
	}
}
