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

	// MOEX tick arrives — VWAP known, but ForexFunding still nil
	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
	snap := e.Snapshot()
	if snap.USDRUBF.ForexFunding != nil {
		t.Error("ForexFunding must remain nil until a forex tick is ingested")
	}

	// Forex tick arrives — ForexFunding becomes non-nil
	e.Ingest(forexTick(source.SymbolEURUSD, 1.085, now))
	snap = e.Snapshot()
	if snap.USDRUBF.ForexFunding == nil {
		t.Error("ForexFunding must be non-nil after a forex tick")
	}
}

func TestEngine_CBFundingNilUntilCBRTick(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
	snap := e.Snapshot()
	if snap.USDRUBF.CBFunding != nil {
		t.Error("CBFunding must be nil before CBR tick")
	}

	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 82.5, now))
	snap = e.Snapshot()
	if snap.USDRUBF.CBFunding == nil {
		t.Error("CBFunding must be non-nil after CBR tick")
	}
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

	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 100, now))
	snap := e.Snapshot()

	// VWAP = 90, LastPrice = 90 → MOEXFunding = 0
	if snap.EURRUBF.MOEXFunding == nil {
		t.Fatal("MOEXFunding must not be nil")
	}
	if *snap.EURRUBF.MOEXFunding != 0.0 {
		t.Errorf("MOEXFunding: want 0.0, got %v", *snap.EURRUBF.MOEXFunding)
	}
}

func TestEngine_ForexFundingValue(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
	e.Ingest(forexTick(source.SymbolEURUSD, 1.085, now))

	snap := e.Snapshot()
	want := 82.0 - 1.085
	if snap.USDRUBF.ForexFunding == nil {
		t.Fatal("ForexFunding must not be nil")
	}
	if diff := *snap.USDRUBF.ForexFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("ForexFunding: want %.6f, got %.6f", want, *snap.USDRUBF.ForexFunding)
	}
}

func TestEngine_CBFundingValue(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	e.Ingest(moexTick(source.SymbolEURRUBF, 91.0, 100, now))
	e.Ingest(cbrTick(source.SymbolEURRubOfficial, 90.0, now))

	snap := e.Snapshot()
	const want = 1.0 // 91 - 90
	if snap.EURRUBF.CBFunding == nil {
		t.Fatal("CBFunding must not be nil")
	}
	if diff := *snap.EURRUBF.CBFunding - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CBFunding: want %.6f, got %.6f", want, *snap.EURRUBF.CBFunding)
	}
}

func TestEngine_CNYRUBFMOEXFunding(t *testing.T) {
	e := funding.NewEngine()
	now := time.Now()

	e.Ingest(moexTick(source.SymbolCNYRUBF, 11.5, 100, now))
	e.Ingest(moexTick(source.SymbolCNYRUBF, 11.8, 50, now.Add(time.Millisecond)))

	snap := e.Snapshot()
	// VWAP = (11.5*100 + 11.8*50) / 150 = (1150 + 590) / 150 = 1740 / 150 = 11.6
	const wantVWAP = 11.6
	if diff := snap.CNYRUBF.VWAP - wantVWAP; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CNYRUBF VWAP: want %.4f, got %.4f", wantVWAP, snap.CNYRUBF.VWAP)
	}
	if snap.CNYRUBF.ForexFunding != nil {
		t.Error("CNYRUBF.ForexFunding should be nil (no CNY/RUB forex feed in MVP)")
	}
	if snap.CNYRUBF.CBFunding != nil {
		t.Error("CNYRUBF.CBFunding should be nil (no CBR CNY rate)")
	}
	if snap.CNYRUBF.MOEXFunding == nil {
		t.Fatal("CNYRUBF.MOEXFunding must not be nil")
	}
	// LastPrice = 11.8 (last tick), VWAP = 11.6 → MOEXFunding ≈ -0.2
	wantMOEX := wantVWAP - 11.8
	if diff := *snap.CNYRUBF.MOEXFunding - wantMOEX; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CNYRUBF MOEXFunding: want %.6f, got %.6f", wantMOEX, *snap.CNYRUBF.MOEXFunding)
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
	now := time.Now()

	e.Ingest(moexTick(source.SymbolUSDRUBF, 82.0, 100, now))
	e.Ingest(moexTick(source.SymbolEURRUBF, 90.0, 100, now))
	e.Ingest(cbrTick(source.SymbolUSDRubOfficial, 82.5, now))

	snap := e.Snapshot()

	// CBFunding available for USDRUBF (has official rate)
	if snap.USDRUBF.CBFunding == nil {
		t.Error("USDRUBF.CBFunding must not be nil")
	}
	// CBFunding still nil for EURRUBF (no EUR official rate ingested yet)
	if snap.EURRUBF.CBFunding != nil {
		t.Error("EURRUBF.CBFunding must be nil when only USD official rate was ingested")
	}
}
