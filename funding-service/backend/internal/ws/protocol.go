package ws

import (
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/funding-service/backend/internal/funding"
)

// WSMessage is the envelope sent over the WebSocket connection.
type WSMessage struct {
	Type      string         `msgpack:"type"` // "snapshot" | "publication" | "ping"
	Timestamp int64          `msgpack:"ts"`   // unix milliseconds
	Payload   map[string]any `msgpack:"payload"`
}

// instrPayload converts one InstrumentFunding to a flat map.
func instrPayload(f funding.InstrumentFunding) map[string]any {
	m := map[string]any{
		"vwap":       f.VWAP,
		"last_price": f.LastPrice,
	}
	if f.MOEXFunding != nil {
		m["moex_funding"] = *f.MOEXFunding
	}
	if f.ForexFunding != nil {
		m["forex_funding"] = *f.ForexFunding
	}
	if f.CBFunding != nil {
		m["cb_funding"] = *f.CBFunding
	}
	if f.OfficialRate != nil {
		m["official_rate"] = *f.OfficialRate
	}
	if f.PredictedFunding != nil {
		m["predicted_funding"] = *f.PredictedFunding
	}
	if f.PredictedCBRate != nil {
		m["predicted_cb_rate"] = *f.PredictedCBRate
	}
	return m
}

// EncodeSnapshot serialises a FundingSnapshot into a MessagePack binary frame.
func EncodeSnapshot(s funding.FundingSnapshot) ([]byte, error) {
	msg := WSMessage{
		Type:      "snapshot",
		Timestamp: s.Timestamp.UnixMilli(),
		Payload: map[string]any{
			"USDRUBF":      instrPayload(s.USDRUBF),
			"EURRUBF":      instrPayload(s.EURRUBF),
			"CNYRUBF":      instrPayload(s.CNYRUBF),
			"usdtrub_price": s.USDTRUBPrice,
		},
	}
	return msgpack.Marshal(msg)
}

// EncodePublication serialises a CBR publication event.
func EncodePublication(symbol string, rate float64, publishedAt time.Time) ([]byte, error) {
	msg := WSMessage{
		Type:      "publication",
		Timestamp: publishedAt.UnixMilli(),
		Payload: map[string]any{
			"symbol": symbol,
			"rate":   rate,
		},
	}
	return msgpack.Marshal(msg)
}
