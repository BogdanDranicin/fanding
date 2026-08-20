package funding_test

import (
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

func moexTick(sym string, price, vol float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Volume:    vol,
		Kind:      source.KindLastPrice,
		Timestamp: ts,
		Source:    "moex-iss",
	}
}

func swapRateTick(sym string, rate float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     rate,
		Kind:      source.KindSwapRate,
		Timestamp: ts,
		Source:    "moex-iss",
	}
}

func cbrTick(sym string, price float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Kind:      source.KindOfficialRate,
		Timestamp: ts,
		Source:    "cbr",
	}
}

// cbrNewTick builds a fresh CBR publication tick (KindNewOfficialRate). Only this kind
// stamps officialRateDate, which the engine compares against today to gate CBFunding.
func cbrNewTick(sym string, price float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Kind:      source.KindNewOfficialRate,
		Timestamp: ts,
		Source:    "cbr",
	}
}

// todaySettle returns today's 15:30 MSK. CBFunding requires the publication to be
// dated "today" (matching wall-clock now), so settlement scenarios must use today's date.
func todaySettle() time.Time {
	msk := time.FixedZone("MSK", 3*60*60)
	now := time.Now().In(msk)
	return time.Date(now.Year(), now.Month(), now.Day(), 15, 30, 0, 0, msk)
}

// tomTick builds a spot TOM LastPrice tick. Volume is cumulative VOLTODAY,
// matching how MOEX ISS reports it; the engine derives incremental weights from deltas.
func tomTick(sym string, price, voltoday float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Volume:    voltoday,
		Kind:      source.KindWaprice,
		Timestamp: ts,
		Source:    "moex-iss",
	}
}

// prevSettleTick builds a KindPrevSettle tick: расчётная цена предыдущего
// вечернего клиринга (PREVSETTLEPRICE у ISS) — база границ K1/K2.
func prevSettleTick(sym string, price float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Kind:      source.KindPrevSettle,
		Timestamp: ts,
		Source:    "moex-iss",
	}
}

// tradeTick builds a KindTrade tick: one executed deal with its own volume.
func tradeTick(sym string, price, qty float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Volume:    qty,
		Kind:      source.KindTrade,
		Timestamp: ts,
		Source:    "moex-iss",
	}
}

// --- exact VWAP from real deals (KindTrade) ---

func TestEngine_TradeTicksFeedExactVWAP(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// ΔVOLTODAY approximation sees a different price (90). Marketdata VOLTODAY
	// grows 20→40; the two real deals below sum to exactly that volume, so the
	// trade feed is complete and its exact VWAP must beat the approximation.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 90.0, 20, now.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 90.0, 40, now.Add(-1*time.Minute)))

	// Real deals: 80×10 and 82×30 → VWAP = (800+2460)/40 = 81.5 exactly.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.0, 10, now.Add(-90*time.Second)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 82.0, 30, now.Add(-30*time.Second)))

	snap := e.Snapshot()
	const want = 81.5
	if diff := snap.USDRUBF.VWAP - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("VWAP: want exact %.4f from trades, got %.6f", want, snap.USDRUBF.VWAP)
	}
}

func TestEngine_TradeVWAPStaleFallsBackToApprox(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// We captured only 10 lots of trades…
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.0, 10, now.Add(-30*time.Minute)))
	// …but marketdata VOLTODAY has since grown to 200 (trade feed died / is lagging
	// far behind real volume) → fall back to the ΔVOLTODAY approximation.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 90.0, 100, now.Add(-1*time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 90.0, 200, now))

	snap := e.Snapshot()
	const want = 90.0
	if diff := snap.USDRUBF.VWAP - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("VWAP: want fallback %.4f (approx), got %.6f", want, snap.USDRUBF.VWAP)
	}
}

func TestEngine_TradeVWAPFreshDespiteOldLastDeal(t *testing.T) {
	// Regression: illiquid instrument (EURRUBF) whose last deal is 12 min old but
	// whose trades already cover the full session volume (Σqty == VOLTODAY). The
	// old wall-clock staleness (>10 min) demoted it to the ΔVOLTODAY fallback, which
	// is empty right after a restart → the displayed VWAP dropped to 0. Freshness is
	// now volume-based, so the exact trade VWAP must still be shown.
	e := funding.NewEngine()
	now := time.Now()

	// Two deals, 40 lots total @ VWAP (80×10+82×30)/40 = 81.5. Last one 12 min ago.
	e.Ingest(tradeTick(source.SymbolEURRUBF, 80.0, 10, now.Add(-15*time.Minute)))
	e.Ingest(tradeTick(source.SymbolEURRUBF, 82.0, 30, now.Add(-12*time.Minute)))
	// A single fresh marketdata tick whose VOLTODAY equals the volume we captured
	// (40): nothing has traded since, so the feed is complete. This lone tick adds
	// nothing to the ΔVOLTODAY approximation (no delta), mimicking a fresh restart.
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 40, now))

	snap := e.Snapshot()
	const want = 81.5
	if diff := snap.EURRUBF.VWAP - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("VWAP: want exact %.4f from complete trade feed, got %.6f (0 = stale-zeroing bug)", want, snap.EURRUBF.VWAP)
	}
}

func TestEngine_PredictedFundingUsesTradeSessionVWAP(t *testing.T) {
	// No KindLastPrice at all (sessionAcc empty) — the trade-based session
	// accumulator alone must drive PredictedFunding.
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	at := func(h, m int) time.Time { return time.Date(2026, 7, 8, h, m, 0, 0, msk) }

	e.Ingest(tomTick(source.SymbolUSDRubTOM, 80.0, 0, at(11, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.0, 10, at(10, 30)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 82.0, 30, at(11, 0)))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedFunding == nil {
		t.Fatal("PredictedFunding must be non-nil from trade session VWAP")
	}
	// sessVWAP=81.5, predRate=80 → d=1.5, capped at l2 = 0.0015×80 = 0.12.
	const want = 0.12
	if diff := *snap.USDRUBF.PredictedFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("PredictedFunding: want %.4f, got %.6f", want, *snap.USDRUBF.PredictedFunding)
	}
}

func TestEngine_SettlementFreezeFromTradeBackfill(t *testing.T) {
	// Restart scenario: no KindLastPrice before 15:30 (sessionAcc can't freeze),
	// but the trade backfill covers the session from open. The freeze must use
	// the trade VWAP as of 15:30 — the 15:31 deal must NOT leak into it.
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()

	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	// Backfilled session deals: VWAP(10:00–15:30) = (80×10 + 82×30)/40 = 81.5.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.0, 10, at(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 82.0, 30, at(12, 0)))
	// First post-15:30 deal triggers the freeze but stays out of the snapshot.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 83.0, 5, at(15, 31)))

	// Заморозка сама по себе наружу не сигналит — её видно по SettlVWAP в снапшоте.
	if settl := e.Snapshot().USDRUBF.SettlVWAP; settl == nil {
		t.Fatal("settlement freeze must happen from trade backfill")
	} else if diff := *settl - 81.5; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("settlVWAP: want 81.5, got %.6f", *settl)
	}

	// CBR publishes today's rate 81.44: d = 81.5 − 81.44 = 0.06, inside the
	// deadband l1 = 0.0814 → CBFunding must be exactly 0. If the 15:31 deal
	// leaked in (VWAP 81.67), d = 0.227 > l1 would give a non-zero funding.
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 81.44, at(16, 35)))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil after settlement freeze + CBR publication")
	}
	if diff := *snap.USDRUBF.CBFunding; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want 0 (deadband), got %.6f — settlement VWAP frozen wrong", diff)
	}
}

// --- predicted CBR rate from spot TOM (CBR methodology: VWAP over 10:00–15:30 MSK) ---

func TestEngine_PredictedCBRateFromSpotTOMWindow(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)

	// WAPRICE from MOEX ISS is already the cumulative session VWAP — the engine
	// just stores the latest value. The second tick (72.0) overwrites the first.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 71.0, 0, time.Date(2026, 5, 29, 11, 0, 0, 0, msk)))
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 72.0, 0, time.Date(2026, 5, 29, 14, 0, 0, 0, msk)))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedCBRate == nil {
		t.Fatal("PredictedCBRate must be non-nil after spot TOM ticks in window")
	}
	// Engine stores latest WAPRICE; second tick wins.
	const want = 72.0
	if diff := *snap.USDRUBF.PredictedCBRate - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("PredictedCBRate: want %.6f, got %.6f", want, *snap.USDRUBF.PredictedCBRate)
	}
}

// Методика ЦБ с 08.06.2026: EUR/RUB(ЦБ) = USD/RUB(ЦБ) × EUR/USD(ЕЦБ) по состоянию на 15:30 МСК.
// Прямого форекс-фида у сервиса нет (TwelveData убран 06.08.2026 вместе с Forex funding),
// поэтому ногу EUR/USD берём из отношения последних официальных курсов ЦБ: ЦБ считает их
// по тому же фиксингу ЕЦБ, так что отношение и есть нужный кросс.
func TestEngine_EURPredictedCBRateFromOfficialRateRatio(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	at := func(h, m int) time.Time { return time.Date(2026, 6, 8, h, m, 0, 0, msk) }

	// USD predicted CB rate из спот TOM (в окне 10:00–15:30) = 80.0.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 80.0, 0, at(14, 0)))
	// Действующие курсы ЦБ дают EUR/USD = 88.0 / 80.0 = 1.10.
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 80.0, at(12, 0)))
	e.Ingest(cbrTick(source.SymbolEURRubOfficial, 88.0, at(12, 0)))

	snap := e.Snapshot()
	if snap.EURRUBF.PredictedCBRate == nil {
		t.Fatal("EURRUBF.PredictedCBRate must be non-nil")
	}
	// EUR/RUB(ЦБ) = 80.0 × 1.10 = 88.0.
	const want = 88.0
	if diff := *snap.EURRUBF.PredictedCBRate - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("EUR PredictedCBRate: want %.6f, got %.6f", want, *snap.EURRUBF.PredictedCBRate)
	}
}

func TestEngine_PredictedFundingFromSpotTOM(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	at := time.Date(2026, 5, 29, 11, 0, 0, 0, msk)

	// Futures session VWAP = 72.0 (VOLTODAY 100→200 = 100 traded @72 in-window).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 72.0, 100, at))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 72.0, 200, at.Add(time.Minute)))
	// Spot TOM in-window VWAP = 71.0 → predicted CBR rate.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 71.0, 100, at))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedFunding == nil {
		t.Fatal("PredictedFunding must be non-nil once futures session VWAP and predicted CBR rate exist")
	}
	// d = 72 − 71 = 1.0; l1 = 0.001·71 = 0.071; l2 = 0.0015·71 = 0.1065.
	// clamp(1.0, 0.071, 0.1065): d>l1 → d−l1=0.929, capped at l2 → 0.1065.
	const want = 0.1065
	if diff := *snap.USDRUBF.PredictedFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("PredictedFunding: want %.6f, got %.6f", want, *snap.USDRUBF.PredictedFunding)
	}
}

func TestEngine_PredictedFundingNilWithoutSpotTOM(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	at := time.Date(2026, 5, 29, 11, 0, 0, 0, msk)

	// Only futures and a CBR rate — but no spot TOM data. PredictedFunding must NOT
	// fall back to the old "futures − yesterday's CBR rate" logic; it stays nil.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 72.0, 100, at))
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 71.0, at))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedFunding != nil {
		t.Errorf("PredictedFunding must be nil without spot TOM data, got %v", *snap.USDRUBF.PredictedFunding)
	}
}

func TestEngine_SpotTOMWindowFreezePreferredOverLive(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)

	// In-window tick (the fixing predictor) followed by a post-15:30 tick. The frozen
	// in-window value must be preferred; the later live tick must NOT contaminate it.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 71.0, 100, time.Date(2026, 5, 29, 12, 0, 0, 0, msk)))
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 99.0, 200, time.Date(2026, 5, 29, 16, 0, 0, 0, msk)))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedCBRate == nil || *snap.USDRUBF.PredictedCBRate != 71.0 {
		t.Errorf("PredictedCBRate must prefer the frozen 10:00–15:30 value 71.0, got %v", snap.USDRUBF.PredictedCBRate)
	}
}

// TestEngine_SpotTOMWindowResetsNextDay guards the daily reset of the frozen
// 10:00–15:30 predictor. Yesterday's fixing must not survive into a new MSK day:
// before today's window fills, the predicted rate has to fall back to the fresh
// live WAPRICE. Without the reset the stale frozen value was preferred and produced
// a large spurious «ошибка прогноза» — the reported "wrong rates" bug.
func TestEngine_SpotTOMWindowResetsNextDay(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)

	// Day 1: an in-window tick freezes the predictor at 71.0.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 71.0, 100, time.Date(2026, 5, 29, 12, 0, 0, 0, msk)))

	// Day 2, before the 10:00–15:30 window opens (ЕТС morning): only a live tick at 85.0.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 85.0, 150, time.Date(2026, 5, 30, 9, 0, 0, 0, msk)))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedCBRate == nil || *snap.USDRUBF.PredictedCBRate != 85.0 {
		t.Errorf("PredictedCBRate must reset on a new day and fall back to today's live 85.0, got %v (stale 71.0 would leak across midnight)", snap.USDRUBF.PredictedCBRate)
	}
}

func TestEngine_SpotTOMLiveFallbackWhenNoWindowData(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)

	// Late start: only post-15:30 ticks arrive (no 10:00–15:30 fixing captured). The
	// predicted row must fall back to the latest live WAPRICE instead of being empty.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 98.0, 100, time.Date(2026, 5, 29, 16, 0, 0, 0, msk)))
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 99.0, 200, time.Date(2026, 5, 29, 17, 0, 0, 0, msk)))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedCBRate == nil || *snap.USDRUBF.PredictedCBRate != 99.0 {
		t.Errorf("PredictedCBRate must fall back to latest live WAPRICE 99.0 on a late start, got %v", snap.USDRUBF.PredictedCBRate)
	}
}

// CBFunding — всегда НАШ расчёт от курса ЦБ. SWAPRATE (что начисляет MOEX) живёт
// в отдельном поле MOEXFunding и НЕ подменяет CBFunding даже после вечернего
// клиринга: раздельные источники позволяют сверять наш расчёт с биржевым.
func TestEngine_CBFundingIsReconstructionNotSwapRate(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Settlement + today's CBR publication → reconstruction is available:
	// d = 82.0 − 81.0 = 1.0 > l1 → capped at l2 = 0.0015 × 81.0 = 0.1215.
	// VOLTODAY 100→200 in-window gives the session VWAP its weight.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle))
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 81.0, settle))

	// MOEX publishes SWAPRATE at the evening clearing — MOEXFunding, not CBFunding.
	e.Ingest(swapRateTick(source.SymbolUSDRUBF, 0.12345, settle.Add(3*time.Hour+30*time.Minute)))

	snap := e.Snapshot()
	if snap.USDRUBF.MOEXFunding == nil || *snap.USDRUBF.MOEXFunding != 0.12345 {
		t.Errorf("MOEXFunding must equal published SWAPRATE 0.12345, got %v", snap.USDRUBF.MOEXFunding)
	}
	const want = 0.1215
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil: settlement + CBR publication happened")
	}
	if diff := *snap.USDRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want own reconstruction %.4f, got %.6f (SWAPRATE must not leak in)", want, *snap.USDRUBF.CBFunding)
	}
}

// Вчерашний SWAPRATE, который ISS отдаёт весь день, НЕ должен заслонять раннюю
// реконструкцию (settle − новый курс ЦБ) после публикации курса ЦБ: значение
// SWAPRATE не менялось после 15:30 — оно протухшее (баг «курсы вышли, а
// результата нет»).
func TestEngine_CBFundingStaleSwapRateYieldsToReconstruction(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Yesterday's SWAPRATE observed since morning; the repeat after 15:30 carries
	// the SAME value — not a clearing publication, must not count as fresh.
	e.Ingest(swapRateTick(source.SymbolUSDRUBF, 0.00631, settle.Add(-3*time.Hour)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle))
	e.Ingest(swapRateTick(source.SymbolUSDRUBF, 0.00631, settle.Add(time.Hour)))

	// CBR publishes today's rate 81.0: d = 82.0 − 81.0 = 1.0 > l1 = 0.081,
	// capped at l2 = 0.0015 × 81.0 = 0.1215.
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 81.0, settle.Add(2*time.Hour)))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil: reconstruction is available")
	}
	const want = 0.1215
	if diff := *snap.USDRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want reconstruction %.4f, got %.6f (stale SWAPRATE must not win)", want, *snap.USDRUBF.CBFunding)
	}
}

// CBFunding появляется ТОЛЬКО после публикации курса ЦБ. Наличие SWAPRATE
// (вчерашнего или любого) до публикации не должно наполнять строку.
func TestEngine_CBFundingNilBeforeCBRDespiteSwapRate(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	e.Ingest(swapRateTick(source.SymbolUSDRUBF, 0.00631, settle.Add(-3*time.Hour)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Errorf("CBFunding must stay nil before CBR publication (SWAPRATE lives in MOEXFunding), got %v", *snap.USDRUBF.CBFunding)
	}
	if snap.USDRUBF.MOEXFunding == nil || *snap.USDRUBF.MOEXFunding != 0.00631 {
		t.Errorf("MOEXFunding must show SWAPRATE 0.00631, got %v", snap.USDRUBF.MOEXFunding)
	}
}

// Прогноз живёт только до 15:30. После заморозки ноги фьючерса он ничего не
// предсказывает (обе ноги неподвижны), а нога ЦБ у нас с почти не торгуемого спота
// и врёт: 27.07.2026 сайт с 15:30 показывал застывшие −0.117 при факте +0.0235.
// Правильное поведение — пустая строка до публикации курса ЦБ, затем CBFunding.
func TestEngine_PredictedFundingStopsAtSettlement(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	// Predicted CB rate from spot TOM (in window) = 81.0.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 81.0, 0, at(14, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 81.05, 10, at(11, 0)))

	// До 15:30 прогноз есть: d = 0.05 внутри мёртвой зоны l1 = 0.081 → ровно 0.
	snap := e.Snapshot()
	if snap.USDRUBF.PredictedFunding == nil {
		t.Fatal("PredictedFunding must be non-nil before settlement")
	}
	if got := *snap.USDRUBF.PredictedFunding; got > 1e-9 || got < -1e-9 {
		t.Errorf("PredictedFunding before 15:30: want 0 (inside deadband), got %.6f", got)
	}

	// Первая сделка после 15:30 замораживает ногу фьючерса — прогноз исчезает.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 90.0, 100, at(15, 31)))

	snap = e.Snapshot()
	if snap.USDRUBF.PredictedFunding != nil {
		t.Errorf("PredictedFunding after settlement: want nil, got %.6f", *snap.USDRUBF.PredictedFunding)
	}
	// Курс ЦБ ещё не опубликован — точного фандинга тоже нет: строка пустая.
	if snap.USDRUBF.CBFunding != nil {
		t.Errorf("CBFunding: want nil before CBR publication, got %.6f", *snap.USDRUBF.CBFunding)
	}
}

// После 15:30 VWAP на сайте показывает ЗАМОРОЖЕННУЮ ногу фьючерса: именно на ней
// фиксируется сегодняшний фандинг, и вечерние сделки не должны её двигать.
func TestEngine_VWAPFrozenAfterSettlement(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	// Окно 10:00–15:30: VWAP = (80×10 + 82×30)/40 = 81.5.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.0, 10, at(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 82.0, 30, at(12, 0)))
	// Вечерние сделки по 95 — в роллинговый VWAP попадут, на витрину не должны.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 95.0, 200, at(15, 31)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 95.0, 200, at(19, 0)))

	snap := e.Snapshot()
	const want = 81.5
	if diff := snap.USDRUBF.VWAP - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("VWAP after 15:30: want frozen %.4f, got %.6f", want, snap.USDRUBF.VWAP)
	}
	if snap.USDRUBF.SettlVWAP == nil || *snap.USDRUBF.SettlVWAP != want {
		t.Errorf("SettlVWAP: want %.4f, got %v", want, snap.USDRUBF.SettlVWAP)
	}
}

// Сделки чужого календарного дня, приписанные биржей к сегодняшней сессии
// (выходные торги, опубликованные утренним клирингом понедельника), в цену окна
// не входят. Живой случай 27.07.2026: 1377 таких сделок в окне тянули VWAP
// USDRUBF на 0.0144 вниз, а объём для проверки полноты фида они дают.
func TestEngine_BackdatedTradesExcludedFromWindowVWAP(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	// Сделки выходных: биржа доложила их в сессию в 06:14, цена — прошлой недели.
	backdated := tradeTick(source.SymbolUSDRUBF, 70.0, 60, at(6, 14))
	backdated.Backdated = true
	e.Ingest(backdated)
	// Сегодняшнее окно: VWAP = (80×10 + 82×30)/40 = 81.5.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.0, 10, at(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 82.0, 30, at(12, 0)))
	// VOLTODAY у биржи считает и сделки выходных: 60 + 40 = 100. Полнота фида
	// должна сойтись, иначе движок свалится на пустой ΔVOLTODAY-фолбэк.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, at(12, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 83.0, 5, at(15, 31)))

	snap := e.Snapshot()
	const want = 81.5
	if snap.USDRUBF.SettlVWAP == nil {
		t.Fatal("SettlVWAP must be frozen at 15:30")
	}
	if diff := *snap.USDRUBF.SettlVWAP - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("SettlVWAP: want %.4f (без сделок чужих дней), got %.6f", want, *snap.USDRUBF.SettlVWAP)
	}
}

// Границы K1/K2 биржа масштабирует от РАСЧЁТНОЙ ЦЕНЫ предыдущего вечернего
// клиринга (PREVSETTLEPRICE), а не от курса ЦБ. Регресс на факт 27.07.2026:
// кап EURRUBF = 0.13334 = 0.0015 × 88.89 (PREVSETTLE); от нового курса 88.7602
// получалось 0.13314 — ровно тот зазор, который мы видели в уведомлении.
func TestEngine_CBFundingLimitsFromPrevSettlePrice(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	e.Ingest(prevSettleTick(source.SymbolEURRUBF, 88.89, at(10, 0)))
	// Нога фьючерса 89.01625 — выше курса ЦБ настолько, что фандинг упирается в кап.
	e.Ingest(tradeTick(source.SymbolEURRUBF, 89.01625, 100, at(11, 0)))
	e.Ingest(tradeTick(source.SymbolEURRUBF, 89.0, 5, at(15, 31)))
	e.Ingest(cbrNewTick(source.SymbolEURRubOfficial, 88.7602, at(17, 7)))

	snap := e.Snapshot()
	if snap.EURRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil")
	}
	const want = 0.0015 * 88.89 // 0.133335 → биржа публикует 0.13334
	if diff := *snap.EURRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.8f (кап от расчётной цены), got %.8f", want, *snap.EURRUBF.CBFunding)
	}
}

// Вечерний клиринг (19:00) перезаписывает PREVSETTLEPRICE сегодняшней ценой —
// сегодняшний фандинг от этого меняться не должен: база заморожена на 15:30.
func TestEngine_FundingBaseFrozenAgainstEveningClearing(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	e.Ingest(prevSettleTick(source.SymbolEURRUBF, 88.89, at(10, 0)))
	e.Ingest(tradeTick(source.SymbolEURRUBF, 89.01625, 100, at(11, 0)))
	e.Ingest(tradeTick(source.SymbolEURRUBF, 89.0, 5, at(15, 31)))
	e.Ingest(cbrNewTick(source.SymbolEURRubOfficial, 88.7602, at(17, 7)))
	// Вечерний клиринг: расчётная цена стала сегодняшней.
	e.Ingest(prevSettleTick(source.SymbolEURRUBF, 88.76, at(19, 5)))

	snap := e.Snapshot()
	if snap.EURRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil")
	}
	const want = 0.0015 * 88.89
	if diff := *snap.EURRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.8f (база заморожена на 15:30), got %.8f", want, *snap.EURRUBF.CBFunding)
	}
}

// Регресс на живую сверку 14.07.2026: MOEX масштабирует границы K1/K2 от курса,
// ДЕЙСТВУЮЩЕГО сегодня (76.6213, опубликован накануне), а отклонение d — от
// нового (77.4912). Факт: SWAPRATE = −0.11493 = −0.0015 × 76.6213; границы от
// нового курса давали бы −0.11624.
func TestEngine_CBFundingLimitsFromEffectiveRate(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Обычный день: действующий курс известен движку с утра (обычный опрос ЦБ).
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 76.6213, settle.Add(-5*time.Hour)))
	// Сессия: VWAP = 77.17 (прирост VOLTODAY 100→200 в окне), заморозка на 15:30.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 77.17, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 77.17, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 77.17, 200, settle))
	// Публикация нового курса 77.4912 (на завтра).
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 77.4912, settle.Add(2*time.Hour)))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil")
	}
	// d = 77.17 − 77.4912 = −0.3212 < −l1 → inner = d + l1 = −0.24458,
	// clamp к −l2 = −0.0015 × 76.6213 = −0.11493195.
	const want = -0.11493195
	if diff := *snap.USDRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.8f (границы от действующего курса), got %.8f", want, *snap.USDRUBF.CBFunding)
	}
}

// Тот же расчёт после рестарта, случившегося ПОСЛЕ публикации ЦБ: действующий
// курс движку неоткуда узнать из тиков (officialRate уже завтрашний) — его
// подсевает main из журнала публикаций через SeedEffectiveRates.
func TestEngine_CBFundingLimitsFromSeededEffectiveRate(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	today := settle.Format("2006-01-02")

	e.SeedEffectiveRates(today, map[string]float64{source.SymbolUSDRubOfficial: 76.6213})

	// Бэкфилл сделок восстанавливает сессию: VWAP@15:30 = 77.17.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 77.17, 10, settle.Add(-4*time.Hour)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 78.0, 5, settle.Add(time.Minute)))
	// Публикация уже случилась — холодный старт приносит новый курс с датой сегодня.
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 77.4912, settle.Add(2*time.Hour)))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil")
	}
	const want = -0.11493195
	if diff := *snap.USDRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.8f (границы от засеянного действующего курса), got %.8f", want, *snap.USDRUBF.CBFunding)
	}
}

// Регресс на живые числа 16.07.2026: ЕТС торгует с 07:00, но нога фьючерса в
// фандинге MOEX — VWAP безадресных сделок ТОЛЬКО за 10:00–15:30 МСК. Утренние
// сделки (бэкфилл отдаёт и их) не должны попадать в settlement VWAP, а полнота
// трейд-фида сверяется с ДНЕВНЫМ VOLTODAY (который включает утро): 16.07 наш
// VWAP с утра 78.078 улетал в кап −0.1169 против −0.1000 биржи (VWAP окна 78.140).
func TestEngine_SettlVWAPExcludesMorningTrades(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	// Действующий сегодня курс (опубликован вчера) — база границ K1/K2.
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 77.9568, at(9, 0)))

	// Утренняя сессия ЕТС: 50 лотов @77.60 до 10:00 — НЕ входят в ногу фьючерса.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 77.60, 50, at(8, 0)))
	// Окно 10:00–15:30: 100 лотов @78.1401 — это и есть нога фьючерса.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 78.1401, 100, at(11, 0)))
	// Marketdata видит дневной объём 150 (утро + окно): полнота фида должна
	// считаться от dayV=150, а не от объёма окна (100 < 0.9×150 дал бы ложный
	// фолбэк на пустой ΔVOLTODAY).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.1401, 150, at(11, 0)))
	// Первая сделка после 15:30 замораживает settlement, не попадая в него.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 79.0, 5, at(15, 31)))

	// Публикация нового курса (на завтра): d = 78.1401 − 78.3181 = −0.1780,
	// inner = d + l1 = −0.1780 + 0.001×77.9568 = −0.1000432 — без капа.
	// Старое поведение (утро в VWAP): 77.96 → кап −0.0015×77.9568 = −0.1169352.
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 78.3181, at(16, 56)))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil")
	}
	want := (78.1401 - 78.3181) + 0.001*77.9568
	if diff := *snap.USDRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.7f (нога = VWAP окна 10:00–15:30), got %.7f", want, *snap.USDRUBF.CBFunding)
	}
}

// То же окно для ΔVOLTODAY-фолбэка: тики до 10:00 двигают только базу lastVol,
// а бутстрап не приписывает накопленный с 07:00 объём первой увиденной цене.
func TestEngine_SessionVWAPWindowExcludesMorningVolume(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 77.9568, at(9, 30)))

	// Утро ЕТС: VOLTODAY растёт 1000→4000 до 10:00 — только база, не VWAP.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 77.0, 1000, at(7, 5)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 77.5, 4000, at(9, 0)))
	// Окно: 4000→5000 = 1000 @78.0 и 5000→7000 = 2000 @78.2 →
	// VWAP = (78.0×1000 + 78.2×2000)/3000 = 78.13333…
	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.0, 5000, at(10, 30)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.2, 7000, at(12, 0)))
	// 15:30 — заморозка settlement (сам тик вне окна, дельты нет).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.5, 7000, at(15, 30)))

	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 78.3181, at(17, 0)))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil")
	}
	// d = 78.13333 − 78.3181 = −0.1847667; inner = d + 0.001×77.9568 = −0.1068099.
	// Старое поведение (бутстрап-засев + утро): VWAP 77.675 → кап −0.1169352.
	want := (78.0*1000+78.2*2000)/3000 - 78.3181 + 0.001*77.9568
	if diff := *snap.USDRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.7f (ΔVOLTODAY только из окна 10:00–15:30), got %.7f", want, *snap.USDRUBF.CBFunding)
	}
}

// --- nil / non-nil checks ---

func TestEngine_FundingNilBeforeAnyTicks(t *testing.T) {
	e := funding.NewEngine()
	snap := e.Snapshot()

	if snap.USDRUBF.CBFunding != nil {
		t.Error("USDRUBF.CBFunding: want nil before any CBR tick")
	}
	if snap.USDRUBF.MOEXFunding != nil {
		t.Error("USDRUBF.MOEXFunding: want nil before any MOEX tick")
	}
	if snap.EURRUBF.CBFunding != nil {
		t.Error("EURRUBF.CBFunding: want nil before any CBR tick")
	}
}

// Отсрочка не должна срабатывать, пока поток сделок ещё довозит хвост окна.
//
// 06.08.2026: наша нога USDRUBF вышла 81.51676 против эталонных 81.51729 по сырым
// сделкам ISS — окно, обрезанное ровно на 15:25 (потеряно 2520 контрактов из 244789),
// и −0.00053 к биржевому SWAPRATE. Причина: marketdata обгоняет фид сделок, тик с
// рыночным временем 15:45 приходил раньше последних сделок окна и включал аварийную
// ветку заморозки. Проверка по объёму этого не ловит: 5 минут — около 1% дня.
func TestEngine_SettlNotFrozenWhileTradesStillCatchingUp(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	// Сделки довезены только до 15:25 — хвост окна ещё в пути.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.0, 900, at(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 81.0, 100, at(15, 25)))
	// Полнота по объёму высокая: marketdata видит 1010 при наших 1000 (99%).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 81.0, 1000, at(15, 25)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 81.0, 1010, at(15, 26)))

	// Marketdata убегает за 15:45 — раньше на этом тике нога и замерзала обрезанной.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 1010, at(15, 46)))
	if settl := e.Snapshot().USDRUBF.SettlVWAP; settl != nil {
		t.Fatalf("нога заморожена раньше времени на %.6f — хвост окна ещё не доехал", *settl)
	}

	// Дошла первая сделка хвоста, затем сделка после 15:30 — теперь можно морозить.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 90.0, 1000, at(15, 29)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 99.0, 100, at(15, 31)))

	settl := e.Snapshot().USDRUBF.SettlVWAP
	if settl == nil {
		t.Fatal("нога должна замёрзнуть, когда поток сделок сам перешагнул 15:30")
	}
	// VWAP окна = (80×900 + 81×100 + 90×1000) / 2000 = 85.05; сделка 15:31 не входит.
	const want = 85.05
	if diff := *settl - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("settlVWAP: want %.6f (весь хвост окна учтён), got %.6f", want, *settl)
	}
}

// Сигнала о заморозке ноги фьючерса (SettlementSignal) у движка больше нет:
// единственным его потребителем был Telegram-диспетчер со служебным
// «Сервис перезапущен», а это сообщение убрано 06.08.2026 — оно приходило
// каждый день и ничего не говорило по делу. Сама заморозка (settlVWAP) жива
// и проверяется тестами вокруг maybeFreezeSettl.

func TestEngine_CBFundingNilWhenSessionStartedAfterSettlement(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	// Service restarted at 17:00 MSK — after 15:30. The first tick that arrives
	// is post-settlement; the engine must NOT freeze settlVWAP from a single tick.
	after := time.Date(2026, 5, 20, 17, 0, 0, 0, msk)

	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, after))
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 82.5, after))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Errorf("CBFunding must be nil when session started after 15:30 MSK, got %v", snap.USDRUBF.CBFunding)
	}
}

func TestEngine_CBFundingNilBeforeSettlement(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	// Tick at 15:29 MSK — settlement freeze has NOT yet happened.
	before := time.Date(2026, 5, 20, 15, 29, 0, 0, msk)

	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, before))
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 82.5, before))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Error("CBFunding must be nil before 15:30 MSK settlement freeze")
	}
}

func TestEngine_CBFundingNilUntilCBRTick(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Normally-running service: pre-settlement ticks set startedPre1530=true and
	// give the accumulator in-window weight (VOLTODAY 100→200).
	// Settlement tick at 15:30 sets settlVWAP (sentinel for post-settlement state).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle))

	// No CBR rate yet — CBFunding must be nil even though settlement happened.
	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Error("CBFunding must be nil before CBR rate tick")
	}

	// Fresh CBR publication today — CBFunding becomes non-nil: clamp(settle - CBR_rate, K1, K2).
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 82.5, settle))
	snap = e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Error("CBFunding must be non-nil after CBR rate tick when settlement has occurred")
	}
	// OfficialRate is set by the same CBR tick.
	if snap.USDRUBF.OfficialRate == nil || *snap.USDRUBF.OfficialRate != 82.5 {
		t.Errorf("OfficialRate: want 82.5, got %v", snap.USDRUBF.OfficialRate)
	}
}

// --- value correctness ---

func TestEngine_VWAPUpdatedByMOEXTicks(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// Volume is cumulative VOLTODAY; the rolling VWAP is weighted by the increment per
	// tick. The first tick only sets the baseline (no attributable volume yet), then
	// VOLTODAY 100→101 = 1 traded @80 and 101→104 = 3 traded @90 → (80 + 270)/4 = 87.5.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 79.0, 100, now))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 80.0, 101, now.Add(time.Millisecond)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 90.0, 104, now.Add(2*time.Millisecond)))

	snap := e.Snapshot()
	const want = 87.5
	if snap.USDRUBF.VWAP != want {
		t.Errorf("VWAP: want %.4f, got %.4f", want, snap.USDRUBF.VWAP)
	}
}

func TestEngine_RollingVWAPUsesVoltodayDeltas(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// A single tick sets only the VOLTODAY baseline — no attributable volume yet, so the
	// rolling VWAP is not available (matches a fresh window right after a restart).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 100.0, 5000, now))
	if v := e.Snapshot().USDRUBF.VWAP; v != 0 {
		t.Errorf("VWAP after a single baseline tick: want 0, got %v", v)
	}

	// VOLTODAY 5000→5010 = 10 traded @101 → VWAP=101 (weighted by the delta 10, not 5010).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 101.0, 5010, now.Add(time.Second)))
	if v := e.Snapshot().USDRUBF.VWAP; v != 101.0 {
		t.Errorf("VWAP weighted by delta: want 101.0, got %v", v)
	}

	// VOLTODAY reset (new trading day): a smaller value than before must not add a
	// negative weight — the drop is skipped and only the new baseline is stored.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 200.0, 5, now.Add(2*time.Second)))
	if v := e.Snapshot().USDRUBF.VWAP; v != 101.0 {
		t.Errorf("VWAP must ignore a VOLTODAY reset (negative delta): want 101.0, got %v", v)
	}

	// Deltas resume from the reset baseline: 5→15 = 10 traded @200. The window still
	// holds the earlier 10@101 → VWAP = (101*10 + 200*10)/20 = 150.5.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 200.0, 15, now.Add(3*time.Second)))
	if v := e.Snapshot().USDRUBF.VWAP; v != 150.5 {
		t.Errorf("VWAP after reset baseline + delta: want 150.5, got %v", v)
	}
}

func TestEngine_MOEXFundingValue(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// MOEXFunding is nil until a KindSwapRate tick arrives
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 100, now))
	snap := e.Snapshot()
	if snap.EURRUBF.MOEXFunding != nil {
		t.Error("MOEXFunding must be nil before a swap_rate tick")
	}

	// KindSwapRate tick sets MOEXFunding to the official MOEX value
	e.Ingest(swapRateTick(source.SymbolEURRUBF, 0.42, now))
	snap = e.Snapshot()
	if snap.EURRUBF.MOEXFunding == nil {
		t.Fatal("MOEXFunding must not be nil after swap_rate tick")
	}
	if *snap.EURRUBF.MOEXFunding != 0.42 {
		t.Errorf("MOEXFunding: want 0.42, got %v", *snap.EURRUBF.MOEXFunding)
	}
}

func TestEngine_CBFundingValue(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Settlement at 15:30 with VWAP = 91.0 (VOLTODAY 100→200 in-window).
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 200, settle))

	// CBFunding = clamp(91.0 − 90.0, K1*90.0, K2*90.0) = clamp(1.0, 0.09, 0.135) = 0.135.
	e.Ingest(cbrNewTick(source.SymbolEURRubOfficial, 90.0, settle.Add(time.Hour)))
	snap := e.Snapshot()
	if snap.EURRUBF.CBFunding == nil {
		t.Fatal("CBFunding must not be nil after settlement + CBR rate")
	}
	const want = 0.135
	if diff := *snap.EURRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.6f, got %.6f", want, *snap.EURRUBF.CBFunding)
	}
}

func TestEngine_CBFundingUpdatesOnCBRChange(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Settlement at 15:30 with VWAP = 91.0 (VOLTODAY 100→200 in-window).
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 200, settle))

	// First CBR publication: clamp(91.0 − 90.0, 0.09, 0.135) = 0.135.
	e.Ingest(cbrNewTick(source.SymbolEURRubOfficial, 90.0, settle.Add(time.Hour)))
	snap := e.Snapshot()
	if snap.EURRUBF.CBFunding == nil || *snap.EURRUBF.CBFunding != 0.135 {
		t.Fatalf("CBFunding: want 0.135, got %v", snap.EURRUBF.CBFunding)
	}

	// New CBR rate matches settlement price: clamp(91.0 − 91.0, 0.091, 0.1365) = 0.
	e.Ingest(cbrNewTick(source.SymbolEURRubOfficial, 91.0, settle.Add(2*time.Hour)))
	snap = e.Snapshot()
	if snap.EURRUBF.CBFunding == nil || *snap.EURRUBF.CBFunding != 0.0 {
		t.Errorf("CBFunding: want 0.0 when rate equals settle price, got %v", snap.EURRUBF.CBFunding)
	}
}

func TestEngine_CNYRUBFMOEXFunding(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// Volume is cumulative VOLTODAY; the VWAP weights the increments. Baseline @11.5(100),
	// then 100 traded @11.5 (→200) and 50 traded @11.8 (→250).
	e.Ingest(moexTick(source.SymbolCNYRUBF, 11.5, 100, now))
	e.Ingest(moexTick(source.SymbolCNYRUBF, 11.5, 200, now.Add(time.Millisecond)))
	e.Ingest(moexTick(source.SymbolCNYRUBF, 11.8, 250, now.Add(2*time.Millisecond)))

	snap := e.Snapshot()
	// VWAP = (11.5*100 + 11.8*50) / 150 = 1740 / 150 = 11.6
	const wantVWAP = 11.6
	if diff := snap.CNYRUBF.VWAP - wantVWAP; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CNYRUBF VWAP: want %.4f, got %.4f", wantVWAP, snap.CNYRUBF.VWAP)
	}
	if snap.CNYRUBF.CBFunding != nil {
		t.Error("CNYRUBF.CBFunding should be nil")
	}
	// MOEXFunding is nil until SWAPRATE tick arrives
	if snap.CNYRUBF.MOEXFunding != nil {
		t.Error("CNYRUBF.MOEXFunding must be nil before swap_rate tick")
	}

	// After swap_rate tick, MOEXFunding equals the official MOEX value
	e.Ingest(swapRateTick(source.SymbolCNYRUBF, 0.035, now))
	snap = e.Snapshot()
	if snap.CNYRUBF.MOEXFunding == nil {
		t.Fatal("CNYRUBF.MOEXFunding must not be nil after swap_rate tick")
	}
	if *snap.CNYRUBF.MOEXFunding != 0.035 {
		t.Errorf("CNYRUBF.MOEXFunding: want 0.035, got %v", *snap.CNYRUBF.MOEXFunding)
	}
}

func TestEngine_BidAskDoNotFeedVWAP(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// Only BID tick — should update LastPrice but NOT VWAP
	e.Ingest(source.Tick{
		Symbol:    source.SymbolUSDRUBF,
		Price:     82.0,
		Volume:    100,
		Kind:      source.KindBid,
		Timestamp: now,
		Source:    "moex-iss",
	})

	snap := e.Snapshot()
	if snap.USDRUBF.VWAP != 0 {
		t.Errorf("BID tick must not feed VWAP, want 0, got %v", snap.USDRUBF.VWAP)
	}
	if snap.USDRUBF.LastPrice != 82.0 {
		t.Errorf("LastPrice: want 82.0, got %v", snap.USDRUBF.LastPrice)
	}
}

func settlePriceTick(sym string, price float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Kind:      source.KindSettlePrice,
		Timestamp: ts,
		Source:    "moex-iss",
	}
}

func TestEngine_SettlePriceBeforeSettlementIgnored(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	before := time.Date(2026, 5, 26, 12, 32, 0, 0, msk)

	// SETTLEPRICE before 15:30 is yesterday's value from MOEX ISS — must be ignored.
	e.Ingest(settlePriceTick(source.SymbolUSDRUBF, 82.0, before))
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 82.5, before))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Errorf("CBFunding must be nil when SETTLEPRICE arrives before 15:30 MSK, got %v", snap.USDRUBF.CBFunding)
	}
}

// SETTLEPRICE не является источником settlement: после рестарта ISS кладёт в него
// ТЕКУЩУЮ цену (наблюдалось вживую 14.07: SETTLEPRICE 78.01 при LAST 78.06, тогда
// как честный VWAP@15:30 был 77.17), и этот тик обгоняет бэкфилл сделок, отравляя
// settlVWAP вечерней ценой. Замораживает settlement только maybeFreezeSettl.
func TestEngine_SettlePriceDoesNotFreezeSettlement(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	after := settle.Add(30 * time.Minute)

	// Restart scenario: SETTLEPRICE (current evening price) arrives BEFORE the
	// trades backfill. It must not freeze settlement.
	e.Ingest(settlePriceTick(source.SymbolUSDRUBF, 83.5, after))
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 82.5, after))
	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Errorf("CBFunding must stay nil: SETTLEPRICE must not freeze settlement, got %v", *snap.USDRUBF.CBFunding)
	}

	// The trades backfill then arrives and freezes the TRUE 15:30 VWAP = 82.0:
	// d = 82.0 − 82.5 = −0.5 → capped at −l2 = −0.0015 × 82.5 = −0.12375.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 82.0, 10, settle.Add(-4*time.Hour)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 83.5, 100, after.Add(time.Minute)))

	snap = e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding must be non-nil after trade-backfill freeze + CBR publication")
	}
	const want = -0.12375
	if diff := *snap.USDRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.5f (from true 15:30 VWAP, not SETTLEPRICE), got %.6f", want, *snap.USDRUBF.CBFunding)
	}
}

func TestEngine_USDTRUBPrice(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	e.Ingest(source.Tick{Symbol: source.SymbolUSDTRUB, Price: 93.5, Kind: source.KindLastPrice, Timestamp: now})
	snap := e.Snapshot()
	if snap.USDTRUBPrice != 93.5 {
		t.Errorf("USDTRUBPrice: want 93.5, got %v", snap.USDTRUBPrice)
	}
}

func TestEngine_EURRUBFIndependentFromUSDRUBF(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	pre := settle.Add(-2 * time.Minute)

	// Pre-settlement ticks ensure both accumulators have startedPre1530=true
	// and in-window weight (VOLTODAY 100→200).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, pre))
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 100, pre))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 200, settle))
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 200, settle))
	// CBR rate only for USD — EUR has no CBR rate yet.
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 82.0, settle))

	snap := e.Snapshot()

	// CBFunding for USDRUBF: settlement occurred + CBR rate available.
	if snap.USDRUBF.CBFunding == nil {
		t.Error("USDRUBF.CBFunding must not be nil")
	}
	// CBFunding for EURRUBF: nil — no EUR CBR rate ingested.
	if snap.EURRUBF.CBFunding != nil {
		t.Error("EURRUBF.CBFunding must be nil when no EUR CBR rate was ingested")
	}
}

// --- задержка публичного фида ISS (~15 минут) и заморозка ноги фьючерса ---

// mskDay возвращает момент сегодняшнего дня по МСК: сценарии заморозки должны
// жить в той же дате, что и now, — иначе суточные гейты движка их отбросят.
func mskDay(h, m int) time.Time {
	settl := todaySettle()
	return time.Date(settl.Year(), settl.Month(), settl.Day(), h, m, 0, 0, time.FixedZone("MSK", 3*60*60))
}

// Данные ISS запаздывают ~15 минут, и тик marketdata с рыночным временем 15:30
// приходит РАНЬШЕ, чем поток сделок довозит хвост окна. Морозить ногу по нему
// нельзя: 28.07.2026 так получилось окно, обрезанное на ~15:15 — settl_vwap USD
// 78.55228 против биржевых 78.55453, фандинг −0.06770 вместо −0.06545.
func TestEngine_SettlementWaitsForDelayedTradeFeed(t *testing.T) {
	e := funding.NewEngine()
	e.Ingest(prevSettleTick(source.SymbolUSDRUBF, 78.02, mskDay(9, 0)))

	// Поток сделок пока довёз окно только до 15:15.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 78.50, 200, mskDay(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 78.50, 100, mskDay(15, 15)))

	// А marketdata уже перешагнул 15:30 своим рыночным временем.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.50, 600, mskDay(15, 31)))
	if got := e.Snapshot().USDRUBF.SettlVWAP; got != nil {
		t.Fatalf("нога фьючерса заморожена до того, как поток сделок дошёл до 15:30: %.5f", *got)
	}

	// Хвост окна приезжает следом — он обязан войти в ногу.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 79.30, 300, mskDay(15, 29)))
	// Первая сделка за границей окна: сама в ногу не входит, но разрешает заморозку.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 90.00, 100, mskDay(15, 32)))

	got := e.Snapshot().USDRUBF.SettlVWAP
	if got == nil {
		t.Fatal("нога фьючерса не заморожена после того, как поток сделок перешагнул 15:30")
	}
	want := (78.50*300 + 79.30*300) / 600
	if diff := *got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("settlVWAP = %.6f, ожидалось полное окно 10:00–15:30 = %.6f", *got, want)
	}
}

// Если поток сделок застрял и границу 15:30 так и не перешагнул, ждать вечно нельзя:
// после отсрочки движок морозит ногу тем, что есть. Фандинг нужен к публикации курса
// ЦБ (после 17:30 МСК), так что запас времени всё равно остаётся большой.
//
// Сама отсрочка — 17:00, а не 15:45: пятнадцати минут не хватало нормальному
// отставанию фида, и по этой аварийной ветке морозилось обрезанное окно
// (см. TestEngine_SettlNotFrozenWhileTradesStillCatchingUp).
func TestEngine_SettlementFreezesAfterGraceWhenTradeFeedStalls(t *testing.T) {
	e := funding.NewEngine()
	e.Ingest(prevSettleTick(source.SymbolUSDRUBF, 78.02, mskDay(9, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 78.50, 200, mskDay(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 79.30, 200, mskDay(15, 15)))

	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.90, 400, mskDay(15, 31)))
	if got := e.Snapshot().USDRUBF.SettlVWAP; got != nil {
		t.Fatalf("заморозка раньше отсрочки: %.5f", *got)
	}
	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.90, 400, mskDay(16, 30)))
	if got := e.Snapshot().USDRUBF.SettlVWAP; got != nil {
		t.Fatalf("заморозка раньше отсрочки (16:30): %.5f", *got)
	}

	e.Ingest(moexTick(source.SymbolUSDRUBF, 78.91, 400, mskDay(17, 0)))
	got := e.Snapshot().USDRUBF.SettlVWAP
	if got == nil {
		t.Fatal("нога фьючерса не заморожена даже после истечения отсрочки")
	}
	want := (78.50*200 + 79.30*200) / 400
	if diff := *got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("settlVWAP = %.6f, ожидалось %.6f", *got, want)
	}
}

// Нога, замороженная по отсрочке, — предварительная: окно у неё могло быть
// обрезано, и движок обязан переписать её точным значением, как только поток
// сделок сам перешагнёт 15:30.
//
// Ровно это сломалось 19.08.2026: нога EURRUBF замёрзла на окне до 15:11 (98.67810
// вместо 98.67362), в 17:52 по ней ушёл в телеграм фандинг 0.03367 вместо 0.02919,
// и правильное значение появилось только через пять минут — по случайности, из-за
// суточного сброса аккумулятора, а не потому, что движок его уточнил.
func TestEngine_ProvisionalSettlRefinedWhenTradesCatchUp(t *testing.T) {
	e := funding.NewEngine()
	e.Ingest(prevSettleTick(source.SymbolUSDRUBF, 80.00, mskDay(9, 0)))

	// Поток сделок довёз только начало окна и застрял.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 81.00, 100, mskDay(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 81.00, 100, mskDay(15, 11)))

	// Отсрочка истекла — морозим тем, что есть, и помечаем предварительным.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 81.00, 200, mskDay(17, 0)))
	snap := e.Snapshot()
	if snap.USDRUBF.SettlVWAP == nil {
		t.Fatal("нога не заморожена после истечения отсрочки")
	}
	if got := *snap.USDRUBF.SettlVWAP; got != 81.00 {
		t.Fatalf("предварительная нога = %.5f, ожидалось 81.00000 (обрезанное окно)", got)
	}
	if !snap.USDRUBF.SettlProvisional {
		t.Fatal("нога заморожена по отсрочке, но не помечена предварительной")
	}

	// Хвост окна доехал, следом сделка за 15:30 — доказательство, что окно всё у нас.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 84.00, 200, mskDay(15, 29)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 99.00, 500, mskDay(15, 31)))

	snap = e.Snapshot()
	// Окно: (81×100 + 81×100 + 84×200) / 400 = 82.5; сделка 15:31 в окно не входит.
	const want = 82.50
	if got := *snap.USDRUBF.SettlVWAP; got != want {
		t.Errorf("нога после уточнения = %.5f, ожидалось %.5f", got, want)
	}
	if snap.USDRUBF.SettlProvisional {
		t.Error("уточнённая нога всё ещё помечена предварительной")
	}
}

// Уточнение не должно превращаться в право переписывать ногу задним числом:
// нормально замороженная (по сделке за 15:30) нога неприкосновенна, что бы дальше
// ни приходило. Иначе вечерние сделки уводили бы зафиксированный фандинг.
func TestEngine_FinalSettlNeverOverwritten(t *testing.T) {
	e := funding.NewEngine()
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.00, 100, mskDay(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 82.00, 100, mskDay(15, 29)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 90.00, 100, mskDay(15, 31)))

	snap := e.Snapshot()
	if snap.USDRUBF.SettlVWAP == nil {
		t.Fatal("нога не заморожена сделкой за 15:30")
	}
	if snap.USDRUBF.SettlProvisional {
		t.Fatal("нога заморожена нормально, пометка предварительной не нужна")
	}
	want := *snap.USDRUBF.SettlVWAP

	// Вечерние сделки и повторный переход границы ничего менять не должны.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 95.00, 900, mskDay(18, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 95.00, 900, mskDay(19, 0)))
	if got := *e.Snapshot().USDRUBF.SettlVWAP; got != want {
		t.Errorf("замороженная нога переписана: было %.5f, стало %.5f", want, got)
	}
}

// Уточнение ноги обязано пересчитать и сам фандинг: подписчик получил число по
// обрезанному окну, на сайте после уточнения должно стоять правильное.
func TestEngine_CBFundingFollowsRefinedSettl(t *testing.T) {
	e := funding.NewEngine()
	// База K1/K2 — расчётная цена предыдущего клиринга.
	e.Ingest(prevSettleTick(source.SymbolUSDRUBF, 80.00, mskDay(9, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.68, 100, mskDay(10, 0)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 80.68, 100, mskDay(17, 0))) // заморозка по отсрочке

	// Курс ЦБ вышел, пока нога ещё предварительная.
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 80.50, mskDay(17, 30)))
	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Fatal("CBFunding не посчитан после публикации курса")
	}
	if !snap.USDRUBF.SettlProvisional {
		t.Fatal("нога должна оставаться предварительной до прихода хвоста")
	}
	// d = 80.68 − 80.50 = 0.18; l1 = 0.001×80 = 0.08 → 0.10 (кап l2 = 0.12 не задет)
	if got := *snap.USDRUBF.CBFunding; !nearly(got, 0.10) {
		t.Fatalf("предварительный CBFunding = %.6f, ожидалось 0.100000", got)
	}

	// Хвост окна довёз более низкие цены — фандинг обязан уехать вниз.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.58, 100, mskDay(15, 29)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 80.58, 100, mskDay(15, 31)))

	snap = e.Snapshot()
	if snap.USDRUBF.SettlProvisional {
		t.Error("нога уточнена, пометка должна быть снята")
	}
	// Окно: (80.68×100 + 80.58×100)/200 = 80.63; d = 0.13; 0.13 − 0.08 = 0.05
	if got := *snap.USDRUBF.CBFunding; !nearly(got, 0.05) {
		t.Errorf("CBFunding после уточнения = %.6f, ожидалось 0.050000", got)
	}
}

func nearly(got, want float64) bool { return got-want < 1e-9 && want-got < 1e-9 }

// Точная нога сильнее оценки полноты: если поток сделок сам перешагнул 15:30,
// окно посчитано целиком, и подменять его приближением по ΔVOLTODAY нельзя.
//
// Ровно это случилось 20.08.2026 с USDRUBF. Сверка по сырой ленте ISS: окно
// 10:00–15:30 = 83.47670 (20064 безадресных сделки, 212014 контрактов), курс ЦБ
// 83.3550, база K1/K2 = PREVSETTLEPRICE 85.13, то есть фандинг 0.03657 — ровно
// столько начислила биржа (SWAPRATE 0.036570). А подписчикам ушло 0.03570:
// движок держал точную ногу на руках, но проверка полноты (наш дневной объём
// против VOLTODAY) не сошлась — в неё не попала утренняя сессия ЕТС с 07:00 —
// и заморозка взяла приближение по ΔVOLTODAY, 83.47583. Разница в ноге 0.00087
// один в один перешла в фандинг.
func TestEngine_ExactLegWinsWhenTradesCrossedSettlement(t *testing.T) {
	e := funding.NewEngine()
	e.Ingest(prevSettleTick(source.SymbolUSDRUBF, 85.13, mskDay(9, 0)))

	// Лента marketdata знает про весь день, включая утреннюю сессию с 07:00:
	// к 09:59 по ней уже прошло 30000 контрактов, которых нет в нашей ленте сделок.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 83.40, 30000, mskDay(9, 59)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 83.40, 30100, mskDay(10, 0)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 83.56, 30200, mskDay(15, 20)))

	// Лента сделок принесла само окно — обе сделки, и ни одной утренней.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 83.40, 100, mskDay(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 83.55, 100, mskDay(15, 20)))

	// Сделка за границей окна: в ногу не входит, но доказывает, что окно закрыто.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 90.00, 100, mskDay(15, 32)))

	got := e.Snapshot().USDRUBF.SettlVWAP
	if got == nil {
		t.Fatal("нога фьючерса не заморожена после сделки за 15:30")
	}
	wantExact := (83.40*100 + 83.55*100) / 200  // 83.475 — по сделкам
	wantApprox := (83.40*100 + 83.56*100) / 200 // 83.480 — по ΔVOLTODAY
	if diff := *got - wantExact; diff > 1e-9 || diff < -1e-9 {
		if diff := *got - wantApprox; diff > -1e-9 && diff < 1e-9 {
			t.Fatalf("нога взята приближением по ΔVOLTODAY (%.5f) при полном окне сделок (%.5f)",
				wantApprox, wantExact)
		}
		t.Fatalf("settlVWAP = %.6f, ожидалось точное окно по сделкам %.6f", *got, wantExact)
	}
}

// Полнота по объёму продолжает работать там, где она и нужна: окно на наших
// глазах не закрылось, отсрочка истекла — значит лента сделок могла оказаться
// обрезанной, и приближение по ΔVOLTODAY надёжнее огрызка.
func TestEngine_StaleTradeFeedStillFallsBackAfterGrace(t *testing.T) {
	e := funding.NewEngine()
	e.Ingest(prevSettleTick(source.SymbolUSDRUBF, 85.13, mskDay(9, 0)))

	e.Ingest(moexTick(source.SymbolUSDRUBF, 83.40, 30000, mskDay(9, 59)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 83.40, 30100, mskDay(10, 0)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 83.56, 30200, mskDay(15, 20)))

	// Лента сделок встала в 10:05 и до 15:30 так и не дошла.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 83.40, 100, mskDay(10, 0)))
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 83.41, 100, mskDay(10, 5)))

	e.Ingest(moexTick(source.SymbolUSDRUBF, 83.56, 30200, mskDay(17, 0)))

	snap := e.Snapshot().USDRUBF
	if snap.SettlVWAP == nil {
		t.Fatal("нога не заморожена после истечения отсрочки")
	}
	want := (83.40*100 + 83.56*100) / 200
	if diff := *snap.SettlVWAP - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("settlVWAP = %.6f, ожидалось приближение по ΔVOLTODAY %.6f", *snap.SettlVWAP, want)
	}
	if !snap.SettlProvisional {
		t.Error("нога, замороженная по отсрочке, обязана быть помечена предварительной")
	}
}
