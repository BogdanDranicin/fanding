package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/funding-service/backend/internal/storage"
	"github.com/funding-service/backend/internal/trades"
)

// tradesIngestMu — сообщения применяются к позициям строго по одному: два
// сообщения одного автора, пришедшие одновременно, иначе читали бы одни и те
// же открытые позиции и затирали изменения друг друга.
var tradesIngestMu sync.Mutex

type tradeMessageReq struct {
	ChannelID    int64  `json:"channel_id"`
	ChannelTitle string `json:"channel_title"`
	MsgID        int64  `json:"msg_id"`
	Date         string `json:"date"`
	Text         string `json:"text"`
	HasMedia     bool   `json:"has_media"`
	ReplyTo      *int64 `json:"reply_to"`
	Silent       bool   `json:"silent"`
}

// handleTradeIngest принимает сообщение канала от tg-repost. Адрес лежит под
// /api и виден снаружи, поэтому вход только по общему секрету: без
// TRADES_INGEST_TOKEN на сервере ручка выключена целиком.
func handleTradeIngest(store *storage.Store, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Trades-Token")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req tradeMessageReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		posted, err := time.Parse(time.RFC3339, req.Date)
		if err != nil || req.ChannelID == 0 || req.MsgID <= 0 || strings.TrimSpace(req.ChannelTitle) == "" {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		tradesIngestMu.Lock()
		events, dup, err := store.IngestTradeMessage(r.Context(), storage.TradeMessageIn{
			ChannelID:    req.ChannelID,
			ChannelTitle: strings.TrimSpace(req.ChannelTitle),
			MsgID:        req.MsgID,
			PostedAt:     posted.UTC(),
			Text:         req.Text,
			HasMedia:     req.HasMedia,
			ReplyTo:      req.ReplyTo,
			Silent:       req.Silent,
		})
		tradesIngestMu.Unlock()
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": dup, "events": len(events)})
	}
}

func queryInt(r *http.Request, key string, def, lo, hi int64) int64 {
	v, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil || v < lo || v > hi {
		return def
	}
	return v
}

func handleTradePositions(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		if status != string(trades.StatusClosed) {
			status = string(trades.StatusOpen)
		}
		rows, err := store.ListTradePositions(r.Context(), status, int(queryInt(r, "limit", 200, 1, 1000)))
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

func handleTradeEvents(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		after := queryInt(r, "after", 0, 0, 1<<62)
		rows, err := store.ListTradeEvents(r.Context(), after, int(queryInt(r, "limit", 100, 1, 500)))
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

func handleTradeLastEvent(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := store.LastTradeEventID(r.Context())
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"id": id})
	}
}

type tradePositionReq struct {
	Author     string   `json:"author"`
	Book       string   `json:"book"`
	Ticker     string   `json:"ticker"`
	Direction  string   `json:"direction"`
	Size       string   `json:"size"`
	EntryPrice *float64 `json:"entry_price"`
	Stop       string   `json:"stop"`
	Targets    string   `json:"targets"`
	Status     string   `json:"status"`
	ClosePrice *float64 `json:"close_price"`
	Note       string   `json:"note"`
}

func (q tradePositionReq) position() (trades.Position, error) {
	p := trades.Position{
		Author:     strings.TrimSpace(q.Author),
		Book:       strings.TrimSpace(q.Book),
		Ticker:     strings.TrimSpace(q.Ticker),
		Direction:  trades.Direction(q.Direction),
		Size:       strings.TrimSpace(q.Size),
		EntryPrice: q.EntryPrice,
		Stop:       strings.TrimSpace(q.Stop),
		Targets:    strings.TrimSpace(q.Targets),
		Status:     trades.Status(q.Status),
		ClosePrice: q.ClosePrice,
		Note:       strings.TrimSpace(q.Note),
	}
	switch {
	case p.Author == "" || p.Ticker == "":
		return p, errors.New("нужны автор и тикер")
	case p.Direction != trades.DirLong && p.Direction != trades.DirShort:
		return p, errors.New("направление — long или short")
	case p.Status != trades.StatusOpen && p.Status != trades.StatusClosed:
		return p, errors.New("статус — open или closed")
	}
	for _, f := range []string{p.Author, p.Book, p.Ticker, p.Size, p.Stop, p.Targets} {
		if utf8.RuneCountInString(f) > 120 {
			return p, errors.New("слишком длинное поле")
		}
	}
	if utf8.RuneCountInString(p.Note) > 1000 {
		return p, errors.New("слишком длинная заметка")
	}
	return p, nil
}

// handleTradeSave — создание (без id в адресе) и правка позиции руками.
func handleTradeSave(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req tradePositionReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		p, err := req.position()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if idStr := chi.URLParam(r, "id"); idStr != "" {
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "bad id", http.StatusBadRequest)
				return
			}
			p.ID = id
		}
		tradesIngestMu.Lock()
		saved, err := store.SaveTradePosition(r.Context(), p)
		tradesIngestMu.Unlock()
		switch {
		case errors.Is(err, storage.ErrTradeNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case err != nil:
			http.Error(w, "db error", http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusOK, saved)
		}
	}
}

func handleTradeDelete(store *storage.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		tradesIngestMu.Lock()
		err = store.DeleteTradePosition(r.Context(), id)
		tradesIngestMu.Unlock()
		switch {
		case errors.Is(err, storage.ErrTradeNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case err != nil:
			http.Error(w, "db error", http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}
