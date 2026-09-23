package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/funding-service/backend/internal/trades"
)

// TradeMessageIn — сообщение канала со сделками, как его присылает tg-repost.
type TradeMessageIn struct {
	ChannelID    int64
	ChannelTitle string
	MsgID        int64
	PostedAt     time.Time
	Text         string
	HasMedia     bool
	ReplyTo      *int64
	// Silent — сообщение из прошлого (досыл истории): позиции ведутся, но
	// всплывающего уведомления о нём быть не должно.
	Silent bool
}

// TradeEvent — строка ленты: что случилось с позицией и из какой фразы.
type TradeEvent struct {
	ID         int64      `json:"id"`
	PositionID *int64     `json:"position_id"`
	Author     string     `json:"author"`
	Ticker     string     `json:"ticker"`
	Action     string     `json:"action"`
	Direction  string     `json:"direction"`
	Size       string     `json:"size"`
	Price      *float64   `json:"price"`
	Clause     string     `json:"clause"`
	Message    string     `json:"message"`
	Manual     bool       `json:"manual"`
	Silent     bool       `json:"silent"`
	At         time.Time  `json:"at"`
	CreatedAt  time.Time  `json:"created_at"`
	PostedAt   *time.Time `json:"posted_at"`
}

// ErrTradeNotFound — правка или удаление позиции, которой нет.
var ErrTradeNotFound = errors.New("trade position not found")

const positionCols = `id, author, book, ticker, direction, size, entry_price, stop, targets, status,
	opened_at, closed_at, close_price, note, manual, updated_at, last_text`

func scanPosition(row pgx.Row) (trades.Position, error) {
	var p trades.Position
	var dir, status string
	err := row.Scan(&p.ID, &p.Author, &p.Book, &p.Ticker, &dir, &p.Size, &p.EntryPrice, &p.Stop, &p.Targets,
		&status, &p.OpenedAt, &p.ClosedAt, &p.ClosePrice, &p.Note, &p.Manual, &p.UpdatedAt, &p.LastText)
	p.Direction = trades.Direction(dir)
	p.Status = trades.Status(status)
	return p, err
}

// IngestTradeMessage записывает сообщение, разбирает его и применяет к позициям
// автора — всё одной транзакцией. Повтор того же сообщения (tg-repost досылает
// после рестарта) узнаётся по (канал, номер) и ничего не меняет: dup = true.
func (s *Store) IngestTradeMessage(ctx context.Context, in TradeMessageIn) (events []TradeEvent, dup bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tickers := trades.MessageTickers(in.Text)
	if tickers == nil {
		tickers = []string{}
	}
	var msgRowID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO trade_messages (channel_id, channel_title, msg_id, posted_at, text, has_media, reply_to, tickers)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (channel_id, msg_id) DO NOTHING
		RETURNING id`,
		in.ChannelID, in.ChannelTitle, in.MsgID, in.PostedAt, in.Text, in.HasMedia, in.ReplyTo,
		tickers).Scan(&msgRowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("insert trade message: %w", err)
	}

	var reply []string
	if in.ReplyTo != nil {
		err = tx.QueryRow(ctx, `SELECT tickers FROM trade_messages WHERE channel_id = $1 AND msg_id = $2`,
			in.ChannelID, *in.ReplyTo).Scan(&reply)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("reply tickers: %w", err)
		}
	}

	sigs := trades.Parse(in.Text, reply)
	if len(sigs) == 0 {
		return nil, false, tx.Commit(ctx)
	}

	ledger, err := loadLedger(ctx, tx, in.ChannelTitle)
	if err != nil {
		return nil, false, err
	}
	changes := ledger.Apply(sigs, in.PostedAt, in.Text)

	ids := map[int64]int64{}
	for _, c := range changes {
		p := c.Position
		if err := persistChange(ctx, tx, &p, ids); err != nil {
			return nil, false, err
		}
		sig := c.Signal
		price := sig.Price
		if c.Action == trades.ActOpen && price == nil {
			price = p.EntryPrice
		}
		size := sig.Size
		if c.Action == trades.ActOpen || c.Action == trades.ActAdd {
			size = p.Size
		}
		ev := TradeEvent{
			PositionID: &p.ID,
			Author:     p.Author,
			Ticker:     p.Ticker,
			Action:     string(c.Action),
			Direction:  string(p.Direction),
			Size:       size,
			Price:      price,
			Clause:     sig.Clause,
			Silent:     in.Silent || c.Action == trades.ActStop,
			At:         in.PostedAt,
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO trade_events (message_id, position_id, author, ticker, action, direction, size, price, clause, silent, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING id, created_at`,
			msgRowID, ev.PositionID, ev.Author, ev.Ticker, ev.Action, ev.Direction, ev.Size, ev.Price, ev.Clause,
			ev.Silent, ev.At).Scan(&ev.ID, &ev.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("insert trade event: %w", err)
		}
		events = append(events, ev)
	}
	return events, false, tx.Commit(ctx)
}

// persistChange пишет позицию после сигнала. Отрицательный id — позиция,
// заведённая этим сообщением: первый раз вставляется, дальше обновляется по
// уже выданному базой id.
func persistChange(ctx context.Context, tx pgx.Tx, p *trades.Position, ids map[int64]int64) error {
	if p.ID < 0 {
		if real, ok := ids[p.ID]; ok {
			p.ID = real
		} else {
			tmp := p.ID
			if err := tx.QueryRow(ctx, `
				INSERT INTO trade_positions (author, book, ticker, direction, size, entry_price, stop, targets,
					status, opened_at, closed_at, close_price, last_text, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				RETURNING id`,
				p.Author, p.Book, p.Ticker, string(p.Direction), p.Size, p.EntryPrice, p.Stop, p.Targets,
				string(p.Status), p.OpenedAt, p.ClosedAt, p.ClosePrice, p.LastText, p.UpdatedAt).Scan(&p.ID); err != nil {
				return fmt.Errorf("insert position: %w", err)
			}
			ids[tmp] = p.ID
			return nil
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE trade_positions SET book = $2, direction = $3, size = $4, entry_price = $5, stop = $6,
			targets = $7, status = $8, closed_at = $9, close_price = $10, last_text = $11, updated_at = $12
		WHERE id = $1`,
		p.ID, p.Book, string(p.Direction), p.Size, p.EntryPrice, p.Stop, p.Targets, string(p.Status),
		p.ClosedAt, p.ClosePrice, p.LastText, p.UpdatedAt); err != nil {
		return fmt.Errorf("update position: %w", err)
	}
	return nil
}

func loadLedger(ctx context.Context, tx pgx.Tx, author string) (*trades.Ledger, error) {
	l := &trades.Ledger{Author: author, LastClosed: map[string]trades.Direction{}}
	rows, err := tx.Query(ctx, `SELECT `+positionCols+` FROM trade_positions
		WHERE author = $1 AND status = 'open' ORDER BY id FOR UPDATE`, author)
	if err != nil {
		return nil, fmt.Errorf("load open positions: %w", err)
	}
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		l.Open = append(l.Open, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `SELECT DISTINCT ON (ticker) ticker, direction FROM trade_positions
		WHERE author = $1 AND status = 'closed' ORDER BY ticker, closed_at DESC NULLS LAST`, author)
	if err != nil {
		return nil, fmt.Errorf("load closed directions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tk, dir string
		if err := rows.Scan(&tk, &dir); err != nil {
			return nil, err
		}
		l.LastClosed[tk] = trades.Direction(dir)
	}
	return l, rows.Err()
}

// ListTradePositions — позиции для вкладки «Сделки»: открытые целиком,
// закрытые — последние limit штук.
func (s *Store) ListTradePositions(ctx context.Context, status string, limit int) ([]trades.Position, error) {
	q := `SELECT ` + positionCols + ` FROM trade_positions WHERE status = $1 ORDER BY `
	if status == string(trades.StatusOpen) {
		q += `updated_at DESC`
	} else {
		q += `closed_at DESC NULLS LAST, id DESC`
	}
	q += ` LIMIT $2`
	rows, err := s.pool.Query(ctx, q, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trades.Position{}
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListTradeEvents: afterID > 0 — всё новее него по возрастанию (так страница
// добирает уведомления), иначе последние limit по убыванию (лента).
func (s *Store) ListTradeEvents(ctx context.Context, afterID int64, limit int) ([]TradeEvent, error) {
	q := `SELECT e.id, e.position_id, e.author, e.ticker, e.action, e.direction, e.size, e.price, e.clause,
			COALESCE(left(m.text, 600), ''), e.manual, e.silent, e.at, e.created_at, m.posted_at
		FROM trade_events e LEFT JOIN trade_messages m ON m.id = e.message_id `
	var rows pgx.Rows
	var err error
	if afterID > 0 {
		rows, err = s.pool.Query(ctx, q+`WHERE e.id > $1 ORDER BY e.id LIMIT $2`, afterID, limit)
	} else {
		rows, err = s.pool.Query(ctx, q+`ORDER BY e.id DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TradeEvent{}
	for rows.Next() {
		var e TradeEvent
		if err := rows.Scan(&e.ID, &e.PositionID, &e.Author, &e.Ticker, &e.Action, &e.Direction, &e.Size,
			&e.Price, &e.Clause, &e.Message, &e.Manual, &e.Silent, &e.At, &e.CreatedAt, &e.PostedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastTradeEventID — с какого события странице начинать ждать новые.
func (s *Store) LastTradeEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM trade_events`).Scan(&id)
	return id, err
}

// SaveTradePosition — ручное создание (ID == 0) или правка позиции с сайта.
// Правка помечает позицию ручной и пишет событие в ленту, чтобы было видно,
// что строку поменял человек, а не разбор.
func (s *Store) SaveTradePosition(ctx context.Context, p trades.Position) (trades.Position, error) {
	now := time.Now().UTC()
	p.UpdatedAt = now
	p.Manual = true
	if p.Status == trades.StatusClosed && p.ClosedAt == nil {
		p.ClosedAt = &now
	}
	if p.Status == trades.StatusOpen {
		p.ClosedAt, p.ClosePrice = nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	action := "manual_edit"
	if p.ID == 0 {
		action = "manual_open"
		if p.OpenedAt.IsZero() {
			p.OpenedAt = now
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO trade_positions (author, book, ticker, direction, size, entry_price, stop, targets,
				status, opened_at, closed_at, close_price, note, manual, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, TRUE, $14)
			RETURNING id`,
			p.Author, p.Book, p.Ticker, string(p.Direction), p.Size, p.EntryPrice, p.Stop, p.Targets,
			string(p.Status), p.OpenedAt, p.ClosedAt, p.ClosePrice, p.Note, now).Scan(&p.ID)
	} else {
		ct, execErr := tx.Exec(ctx, `
			UPDATE trade_positions SET author = $2, book = $3, ticker = $4, direction = $5, size = $6,
				entry_price = $7, stop = $8, targets = $9, status = $10, closed_at = $11, close_price = $12,
				note = $13, manual = TRUE, updated_at = $14, opened_at = COALESCE($15, opened_at)
			WHERE id = $1`,
			p.ID, p.Author, p.Book, p.Ticker, string(p.Direction), p.Size, p.EntryPrice, p.Stop, p.Targets,
			string(p.Status), p.ClosedAt, p.ClosePrice, p.Note, now, nullTime(p.OpenedAt))
		if execErr == nil && ct.RowsAffected() == 0 {
			return p, ErrTradeNotFound
		}
		err = execErr
	}
	if err != nil {
		return p, fmt.Errorf("save position: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO trade_events (position_id, author, ticker, action, direction, size, price, manual, silent, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, TRUE, TRUE, $8)`,
		p.ID, p.Author, p.Ticker, action, string(p.Direction), p.Size, p.EntryPrice, now); err != nil {
		return p, fmt.Errorf("manual event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return p, err
	}
	row := s.pool.QueryRow(ctx, `SELECT `+positionCols+` FROM trade_positions WHERE id = $1`, p.ID)
	return scanPosition(row)
}

// DeleteTradePosition удаляет позицию целиком (ошибка разбора). События ленты
// остаются, ссылка на позицию в них обнуляется.
func (s *Store) DeleteTradePosition(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM trade_positions WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrTradeNotFound
	}
	return nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
