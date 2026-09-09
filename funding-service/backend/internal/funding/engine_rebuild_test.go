package funding_test

import (
	"testing"
	"time"

	"github.com/funding-service/backend/internal/funding"
	"github.com/funding-service/backend/internal/source"
)

// atMSK — сегодняшний момент по МСК. Реконструкция ноги привязана к календарной
// дате «сегодня», как и весь расчёт CBFunding.
func atMSK(h, m int) time.Time {
	msk := time.FixedZone("MSK", 3*60*60)
	now := time.Now().In(msk)
	return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, msk)
}

// До 15:50 добирать нечего: публичная лента ISS отстаёт на пятнадцать минут, и
// хвоста окна в ней ещё нет.
func TestSettlNeedsRebuildWaitsForTape(t *testing.T) {
	e := funding.NewEngine()
	if _, need := e.SettlNeedsRebuild(source.SymbolEURRUBF, atMSK(15, 35)); need {
		t.Error("бэкстоп полез в ленту в 15:35, когда хвоста окна в ней заведомо нет")
	}
	if _, need := e.SettlNeedsRebuild(source.SymbolEURRUBF, atMSK(15, 50)); !need {
		t.Error("нога не заморожена, а бэкстоп её не добирает")
	}
}

// Нога, замороженная аварийно (по приближению ΔVOLTODAY), обязана уточниться:
// это и есть боевой случай 09.09.2026.
func TestRebuildReplacesProvisionalLeg(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	// Лента сделок до движка не дошла — нога встала на приближении по VOLTODAY.
	e.Ingest(moexTick(source.SymbolEURRUBF, 99.41258, 100, settle.Add(-2*time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 99.41258, 200, settle.Add(-time.Minute)))
	e.Ingest(moexTick(source.SymbolEURRUBF, 99.41258, 200, settle))

	snap := e.Snapshot()
	if snap.EURRUBF.SettlVWAP == nil {
		t.Fatal("нога не заморожена: сценарий не воспроизведён")
	}
	if !snap.EURRUBF.SettlProvisional {
		t.Fatal("нога по приближению обязана быть предварительной")
	}

	date, need := e.SettlNeedsRebuild(source.SymbolEURRUBF, atMSK(15, 50))
	if !need {
		t.Fatal("предварительную ногу бэкстоп обязан добирать")
	}
	if !e.SetSettlFromTape(source.SymbolEURRUBF, date, 99.50425) {
		t.Fatal("нога по перечитанной ленте не принята")
	}

	snap = e.Snapshot()
	if snap.EURRUBF.SettlProvisional {
		t.Error("после уточнения нога всё ещё предварительная — в бот уйдёт «уточняется»")
	}
	if got := *snap.EURRUBF.SettlVWAP; got != 99.50425 {
		t.Errorf("нога = %.5f, хотим 99.50425", got)
	}
	if got := snap.EURRUBF.SettlSource; got != "iss-rebuild" {
		t.Errorf("источник ноги = %q, хотим iss-rebuild", got)
	}

	// Добрано — второй раз лента не читается.
	if _, need := e.SettlNeedsRebuild(source.SymbolEURRUBF, atMSK(16, 30)); need {
		t.Error("бэкстоп продолжает дёргать ленту после того, как нога стала точной")
	}
}

// Ногу, посчитанную источником, закрывшим окно на наших глазах, бэкстоп не трогает.
func TestRebuildLeavesFinalLegAlone(t *testing.T) {
	e := funding.NewEngine()
	settle := todaySettle()

	e.Ingest(source.Tick{
		Symbol: source.SymbolEURRUBF, Kind: source.KindStreamUp,
		Timestamp: settle.Add(-8 * time.Hour), Source: "tinvest", Live: true,
	})
	e.Ingest(liveTradeTick(source.SymbolEURRUBF, 99.50425, 100, settle.Add(-time.Hour)))
	e.Ingest(liveTradeTick(source.SymbolEURRUBF, 99.60000, 50, settle.Add(time.Second)))

	snap := e.Snapshot()
	if snap.EURRUBF.SettlVWAP == nil || snap.EURRUBF.SettlProvisional {
		t.Fatalf("живая нога не заморожена окончательно: %+v", snap.EURRUBF)
	}
	if _, need := e.SettlNeedsRebuild(source.SymbolEURRUBF, atMSK(15, 50)); need {
		t.Error("бэкстоп лезет в ленту при уже готовой точной ноге")
	}
	if e.SetSettlFromTape(source.SymbolEURRUBF, atMSK(15, 50).Format("2006-01-02"), 1.0) {
		t.Error("бэкстоп переписал окончательную ногу")
	}
	if got := *e.Snapshot().EURRUBF.SettlVWAP; got != 99.50425 {
		t.Errorf("нога = %v, хотим нетронутую 99.50425", got)
	}
}

// Инструмент, за которым сервис не следит, бэкстопу неинтересен.
func TestRebuildIgnoresUntrackedSymbol(t *testing.T) {
	e := funding.NewEngine()
	if _, need := e.SettlNeedsRebuild(source.SymbolUSDRubTOM, atMSK(16, 0)); need {
		t.Error("бэкстоп собрался добирать ногу спота")
	}
	if e.SetSettlFromTape(source.SymbolUSDRubTOM, atMSK(16, 0).Format("2006-01-02"), 80) {
		t.Error("нога поставлена инструменту, за которым нет наблюдения")
	}
}
