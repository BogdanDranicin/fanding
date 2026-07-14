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

func forexTick(sym string, price float64, ts time.Time) source.Tick {
	return source.Tick{
		Symbol:    sym,
		Price:     price,
		Kind:      source.KindLastPrice,
		Timestamp: ts,
		Source:    "twelvedata",
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

	select {
	case <-e.SettlementCh():
	default:
		t.Fatal("settlement signal must fire from trade backfill")
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
// Прогнозный курс EUR должен использовать EUR/USD, ЗАМОРОЖЕННЫЙ на 15:30, а не живой —
// тик EUR/USD после 15:30 не должен менять прогноз.
func TestEngine_EURPredictedCBRateFixesEURUSDAt1530(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	at := func(h, m int) time.Time { return time.Date(2026, 6, 8, h, m, 0, 0, msk) }

	// USD predicted CB rate из спот TOM (в окне 10:00–15:30) = 80.0.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 80.0, 0, at(14, 0)))
	// EUR/USD до 15:30 — это нога фиксинга. Значение на 15:29 должно быть взято в расчёт.
	e.Ingest(forexTick(source.SymbolEURUSD, 1.10, at(15, 29)))
	// Тик EUR/USD после 15:30 НЕ должен сдвигать прогнозный курс EUR ЦБ.
	e.Ingest(forexTick(source.SymbolEURUSD, 1.20, at(16, 0)))

	snap := e.Snapshot()
	if snap.EURRUBF.PredictedCBRate == nil {
		t.Fatal("EURRUBF.PredictedCBRate must be non-nil")
	}
	// EUR/RUB(ЦБ) = 80.0 × 1.10 = 88.0 (а не 96.0 с живым 1.20).
	const want = 88.0
	if diff := *snap.EURRUBF.PredictedCBRate - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("EUR PredictedCBRate: want %.6f (EUR/USD заморожен на 15:30), got %.6f", want, *snap.EURRUBF.PredictedCBRate)
	}
}

func TestEngine_PredictedFundingFromSpotTOM(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)
	at := time.Date(2026, 5, 29, 11, 0, 0, 0, msk)

	// Futures session VWAP = 72.0 (single seeding tick).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 72.0, 100, at))
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
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle))
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
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle))
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
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Errorf("CBFunding must stay nil before CBR publication (SWAPRATE lives in MOEXFunding), got %v", *snap.USDRUBF.CBFunding)
	}
	if snap.USDRUBF.MOEXFunding == nil || *snap.USDRUBF.MOEXFunding != 0.00631 {
		t.Errorf("MOEXFunding must show SWAPRATE 0.00631, got %v", snap.USDRUBF.MOEXFunding)
	}
}

// После 15:30 нога фьючерса в прогнозе замораживается на settlement VWAP:
// послерасчётные сделки не должны тащить прогноз (нога ЦБ уже заморожена,
// живой сессионный VWAP делает ноги несопоставимыми по времени).
func TestEngine_PredictedFundingFrozenAfterSettlement(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()
	mskZone := settle.Location()
	at := func(h, m int) time.Time {
		return time.Date(settle.Year(), settle.Month(), settle.Day(), h, m, 0, 0, mskZone)
	}

	// Predicted CB rate from spot TOM (in window) = 81.0.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 81.0, 0, at(14, 0)))
	// Session deals before 15:30: VWAP = 81.05 → frozen at settlement.
	// d = 0.05 is INSIDE the deadband l1 = 0.081 → prediction must be exactly 0.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 81.05, 10, at(11, 0)))
	// Post-settlement deal at a wildly different price triggers the freeze and
	// must NOT feed the prediction. If it leaked, the live session VWAP would be
	// (81.05×10 + 90×100)/110 ≈ 89.19 → d ≈ 8.19 → capped 0.1215, not 0.
	e.Ingest(tradeTick(source.SymbolUSDRUBF, 90.0, 100, at(15, 31)))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedFunding == nil {
		t.Fatal("PredictedFunding must be non-nil (CBR not published yet)")
	}
	if got := *snap.USDRUBF.PredictedFunding; got > 1e-9 || got < -1e-9 {
		t.Errorf("PredictedFunding: want 0 (frozen at settle, inside deadband), got %.6f", got)
	}
}

// --- nil / non-nil checks ---

func TestEngine_FundingNilBeforeAnyTicks(t *testing.T) {
	e := funding.NewEngine()
	snap := e.Snapshot()

	if snap.USDRUBF.ForexFunding != nil {
		t.Error("USDRUBF.ForexFunding: want nil before any forex tick")
	}
	if snap.USDRUBF.CBFunding != nil {
		t.Error("USDRUBF.CBFunding: want nil before any CBR tick")
	}
	if snap.USDRUBF.MOEXFunding != nil {
		t.Error("USDRUBF.MOEXFunding: want nil before any MOEX tick")
	}
	if snap.EURRUBF.ForexFunding != nil {
		t.Error("EURRUBF.ForexFunding: want nil before any forex tick")
	}
}

func TestEngine_ForexFundingNilUntilForexTick(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// USDRUBF MOEX ticks (cumulative VOLTODAY 100→110) — VWAP known, but no USDCNH/CNYRUBF yet
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 110, now.Add(time.Millisecond)))
	snap := e.Snapshot()
	if snap.USDRUBF.ForexFunding != nil {
		t.Error("ForexFunding must be nil until both USDCNH and CNYRUBF are ingested")
	}

	// USDCNH tick — still nil (no CNYRUBF last price yet)
	e.Ingest(forexTick(source.SymbolUSDCNH, 7.5, now))
	snap = e.Snapshot()
	if snap.USDRUBF.ForexFunding != nil {
		t.Error("ForexFunding must remain nil until CNYRUBF last price is ingested")
	}

	// CNYRUBF tick — all components available; ForexFunding becomes non-nil
	e.Ingest(moexTick(source.SymbolCNYRUBF, 10.5, 100, now))
	snap = e.Snapshot()
	if snap.USDRUBF.ForexFunding == nil {
		t.Error("ForexFunding must be non-nil after USDCNH and CNYRUBF are both ingested")
	}
}

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

	// Normally-running service: pre-settlement tick sets startedPre1530=true.
	// Settlement tick at 15:30 sets settlVWAP (sentinel for post-settlement state).
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle))

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

func TestEngine_ForexFundingValue(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	// ForexFunding(USDRUBF) = VWAP - USDCNH*CNYRUBF_last = 82 - 8*10 = 2.0.
	// Two USDRUBF ticks (cumulative VOLTODAY 100→110) give the rolling VWAP an
	// attributable increment: 10 traded @82 → VWAP=82.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 110, now.Add(time.Millisecond)))
	e.Ingest(forexTick(source.SymbolUSDCNH, 8.0, now))
	e.Ingest(moexTick(source.SymbolCNYRUBF, 10.0, 100, now))

	snap := e.Snapshot()
	want := 82.0 - 8.0*10.0
	if snap.USDRUBF.ForexFunding == nil {
		t.Fatal("ForexFunding must not be nil")
	}
	if diff := *snap.USDRUBF.ForexFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("ForexFunding: want %.6f, got %.6f", want, *snap.USDRUBF.ForexFunding)
	}
}

func TestEngine_CBFundingValue(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Settlement at 15:30 with VWAP = 91.0.
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 100, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 100, settle))

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

	// Settlement at 15:30 with VWAP = 91.0.
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 100, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 100, settle))

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
	if snap.CNYRUBF.ForexFunding != nil {
		t.Error("CNYRUBF.ForexFunding should be nil (taken from MOEX, not computed)")
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
	pre := settle.Add(-time.Minute)

	// Pre-settlement ticks ensure both accumulators have startedPre1530=true.
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, pre))
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 100, pre))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, settle))
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 100, settle))
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
