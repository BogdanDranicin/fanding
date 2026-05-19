package ws

import (
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/funding-service/backend/internal/funding"
)

func ptr(f float64) *float64 { return &f }

func TestEncodeSnapshot_roundtrip(t *testing.T) {
	snap := funding.FundingSnapshot{
		Timestamp: time.UnixMilli(1_700_000_000_000),
		USDRUBF: funding.InstrumentFunding{
			VWAP:         88.5,
			LastPrice:    88.1,
			MOEXFunding:  ptr(0.4),
			CBFunding:    ptr(0.3),
			OfficialRate: ptr(88.2),
		},
		EURRUBF: funding.InstrumentFunding{
			VWAP:      96.0,
			LastPrice: 95.5,
		},
		CNYRUBF: funding.InstrumentFunding{
			VWAP:        12.1,
			LastPrice:   12.0,
			MOEXFunding: ptr(0.1),
		},
		USDTRUBPrice: 0,
	}

	data, err := EncodeSnapshot(snap)
	if err != nil {
		t.Fatalf("EncodeSnapshot error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("encoded data is empty")
	}

	var msg WSMessage
	if err := msgpack.Unmarshal(data, &msg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if msg.Type != "snapshot" {
		t.Errorf("Type = %q, want %q", msg.Type, "snapshot")
	}
	if msg.Timestamp != snap.Timestamp.UnixMilli() {
		t.Errorf("Timestamp = %d, want %d", msg.Timestamp, snap.Timestamp.UnixMilli())
	}
	if msg.Payload == nil {
		t.Fatal("Payload is nil")
	}

	usdrubf, ok := msg.Payload["USDRUBF"]
	if !ok {
		t.Fatal("Payload missing USDRUBF")
	}
	instr, ok := usdrubf.(map[string]any)
	if !ok {
		t.Fatalf("USDRUBF payload type %T, want map[string]any", usdrubf)
	}
	if instr["moex_funding"] == nil {
		t.Error("USDRUBF moex_funding missing from payload")
	}
	if instr["official_rate"] == nil {
		t.Error("USDRUBF official_rate missing from payload")
	}

	// nil pointer fields must be absent
	eurrubf := msg.Payload["EURRUBF"].(map[string]any)
	if _, found := eurrubf["moex_funding"]; found {
		t.Error("EURRUBF moex_funding should be absent when nil")
	}
}

func TestEncodePublication(t *testing.T) {
	ts := time.UnixMilli(1_700_000_500_000)
	data, err := EncodePublication("USDRUB", 88.5, ts)
	if err != nil {
		t.Fatalf("EncodePublication error: %v", err)
	}

	var msg WSMessage
	if err := msgpack.Unmarshal(data, &msg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if msg.Type != "publication" {
		t.Errorf("Type = %q, want %q", msg.Type, "publication")
	}
	if msg.Payload["symbol"] != "USDRUB" {
		t.Errorf("symbol = %v", msg.Payload["symbol"])
	}
}
