package ws

import (
	"encoding/json"
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
	// Нога фьючерса на 15:30 и то, чем она посчитана. Уходит наружу, чтобы сверку
	// с биржей можно было делать по снапшоту, не поднимая логи: "live" — окно
	// закрыто живым потоком сделок брокера в свою же секунду, "iss-trades" —
	// точной лентой MOEX ISS (она отстаёт на четверть часа), "voltoday" —
	// приближением по приросту VOLTODAY.
	if f.SettlVWAP != nil {
		m["settl_vwap"] = *f.SettlVWAP
	}
	if f.SettlSource != "" {
		m["settl_source"] = f.SettlSource
		m["settl_provisional"] = f.SettlProvisional
	}
	return m
}

// feedPayload описывает источник сделок: живой поток брокера или публичная
// лента MOEX ISS, отстающая на пятнадцать минут. Страница пишет это словами,
// чтобы «свежая цифра» и «цифра четвертьчасовой давности» перестали выглядеть
// одинаково.
func feedPayload(f funding.FeedStatus) map[string]any {
	return map[string]any{
		"live":    f.Live,
		"lag_ms":  f.LagMs,
		"symbols": f.Symbols,
	}
}

// EncodeSnapshot serialises a FundingSnapshot into a MessagePack binary frame.
func EncodeSnapshot(s funding.FundingSnapshot) ([]byte, error) {
	msg := WSMessage{
		Type:      "snapshot",
		Timestamp: s.Timestamp.UnixMilli(),
		Payload: map[string]any{
			"USDRUBF":       instrPayload(s.USDRUBF),
			"EURRUBF":       instrPayload(s.EURRUBF),
			"CNYRUBF":       instrPayload(s.CNYRUBF),
			"usdtrub_price": s.USDTRUBPrice,
			"feed":          feedPayload(s.Feed),
		},
	}
	return msgpack.Marshal(msg)
}

// SnapshotJSON serialises a FundingSnapshot as JSON with the same field layout
// as the WebSocket frame. Used by the GET /api/v1/snapshot diagnostic so cbrwatch
// can read the live predicted_cb_rate and official_rate over plain HTTP.
func SnapshotJSON(s funding.FundingSnapshot) ([]byte, error) {
	payload := map[string]any{
		"ts":            s.Timestamp.UnixMilli(),
		"USDRUBF":       instrPayload(s.USDRUBF),
		"EURRUBF":       instrPayload(s.EURRUBF),
		"CNYRUBF":       instrPayload(s.CNYRUBF),
		"usdtrub_price": s.USDTRUBPrice,
		"feed":          feedPayload(s.Feed),
	}
	return json.Marshal(payload)
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
