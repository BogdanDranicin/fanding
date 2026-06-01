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

func TestEngine_SpotTOMOutsideWindowIgnored(t *testing.T) {
	e := funding.NewEngine()
	msk := time.FixedZone("MSK", 3*60*60)

	// 09:30 is before 10:00; 16:00 is after 15:30 — both outside the CBR window, must be ignored.
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 71.0, 100, time.Date(2026, 5, 29, 9, 30, 0, 0, msk)))
	e.Ingest(tomTick(source.SymbolUSDRubTOM, 99.0, 200, time.Date(2026, 5, 29, 16, 0, 0, 0, msk)))

	snap := e.Snapshot()
	if snap.USDRUBF.PredictedCBRate != nil {
		t.Errorf("PredictedCBRate must be nil when all spot ticks fall outside 10:00–15:30 MSK, got %v", *snap.USDRUBF.PredictedCBRate)
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

	// USDRUBF MOEX tick — VWAP known, but no USDCNH or CNYRUBF yet
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
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

	// price=80 vol=1, price=90 vol=3 → VWAP = (80 + 270) / 4 = 87.5
	e.Ingest(moexTick(source.SymbolUSDRUBF, 80.0, 1, now))
	e.Ingest(moexTick(source.SymbolUSDRUBF, 90.0, 3, now.Add(time.Millisecond)))

	snap := e.Snapshot()
	const want = 87.5
	if snap.USDRUBF.VWAP != want {
		t.Errorf("VWAP: want %.4f, got %.4f", want, snap.USDRUBF.VWAP)
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

	// ForexFunding(USDRUBF) = VWAP - USDCNH*CNYRUBF_last = 82 - 8*10 = 2.0
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
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

	e.Ingest(moexTick(source.SymbolCNYRUBF, 11.5, 100, now))
	e.Ingest(moexTick(source.SymbolCNYRUBF, 11.8, 50, now.Add(time.Millisecond)))

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

func TestEngine_SettlePriceAfterSettlementAccepted(t *testing.T) {
	e := funding.NewEngine()
	after := todaySettle().Add(30 * time.Minute) // 16:00 MSK today, after settlement

	// SETTLEPRICE after 15:30 is today's settlement price — must activate CBFunding.
	e.Ingest(settlePriceTick(source.SymbolUSDRUBF, 82.0, after))
	e.Ingest(cbrNewTick(source.SymbolUSDRubOfficial, 82.5, after))

	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Error("CBFunding must not be nil when SETTLEPRICE arrives after 15:30 MSK with CBR rate")
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
