package source_test

import (
	"testing"
	"time"

	"github.com/funding-service/backend/internal/source"
)

func TestTickKindValues(t *testing.T) {
	kinds := []source.TickKind{
		source.KindLastPrice,
		source.KindBid,
		source.KindAsk,
		source.KindOfficialRate,
		source.KindVWAP,
	}
	seen := make(map[source.TickKind]bool)
	for _, k := range kinds {
		if seen[k] {
			t.Fatalf("duplicate TickKind value: %d", k)
		}
		seen[k] = true
	}
}

func TestTickFields(t *testing.T) {
	now := time.Now()
	tick := source.Tick{
		Symbol:    source.SymbolUSDRUBF,
		Price:     85.5,
		Volume:    1000,
		Kind:      source.KindLastPrice,
		Timestamp: now,
		Source:    "moex",
	}

	if tick.Symbol != source.SymbolUSDRUBF {
		t.Errorf("unexpected symbol: %s", tick.Symbol)
	}
	if tick.Price != 85.5 {
		t.Errorf("unexpected price: %f", tick.Price)
	}
	if tick.Kind != source.KindLastPrice {
		t.Errorf("unexpected kind: %d", tick.Kind)
	}
	if !tick.Timestamp.Equal(now) {
		t.Errorf("unexpected timestamp")
	}
}

func TestSymbolConstants(t *testing.T) {
	symbols := []string{
		source.SymbolUSDRUBF,
		source.SymbolEURRUBF,
		source.SymbolCNYRUBF,
		source.SymbolUSDTRUB,
		source.SymbolEURUSD,
		source.SymbolUSDCNH,
		source.SymbolUSDRubOfficial,
		source.SymbolEURRubOfficial,
	}
	seen := make(map[string]bool)
	for _, s := range symbols {
		if s == "" {
			t.Fatal("empty symbol constant")
		}
		if seen[s] {
			t.Fatalf("duplicate symbol constant: %s", s)
		}
		seen[s] = true
	}
}
