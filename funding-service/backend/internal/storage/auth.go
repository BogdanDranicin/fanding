package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrLinkTokenNotFound — токен из deep-link не найден: ссылка протухла (аккаунт,
// которому она принадлежала, уже слит с другим) или её просто выдумали.
var ErrLinkTokenNotFound = errors.New("link token not found")

// AuthUser — то, что сессия знает о своём владельце. Один аккаунт = один
// Telegram-чат; браузеров у аккаунта может быть сколько угодно.
type AuthUser struct {
	UserID   int64
	Linked   bool   // чат Telegram привязан — можно слать уведомления
	IsAdmin  bool   // доступны админские вкладки («Журнал», «Скорость»)
	Username string // telegram_username, если Telegram его отдал
}

// randomToken returns a 32-hex-char cryptographically random token.
func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// CreateSession заводит анонимный аккаунт и первую сессию для нового браузера.
// Возвращает токен сессии — браузер кладёт его в localStorage и шлёт в
// Authorization: Bearer. Аккаунт становится «привязанным» позже, из бота.
func (s *Store) CreateSession(ctx context.Context) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var userID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&userID); err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO sessions (token, user_id) VALUES ($1, $2)`, token, userID); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return token, nil
}

// SessionUser разрешает токен сессии в её владельца и попутно двигает last_seen_at.
// Возвращает pgx.ErrNoRows, если токена нет (сессия удалена вместе с аккаунтом).
func (s *Store) SessionUser(ctx context.Context, token string) (AuthUser, error) {
	var u AuthUser
	var username *string
	var chatID *int64
	err := s.pool.QueryRow(ctx, `
		UPDATE sessions SET last_seen_at = now()
		WHERE token = $1
		RETURNING user_id,
		          (SELECT telegram_chat_id  FROM users WHERE id = sessions.user_id),
		          (SELECT is_admin          FROM users WHERE id = sessions.user_id),
		          (SELECT telegram_username FROM users WHERE id = sessions.user_id)`,
		token,
	).Scan(&u.UserID, &chatID, &u.IsAdmin, &username)
	if err != nil {
		return AuthUser{}, err
	}
	u.Linked = chatID != nil
	if username != nil {
		u.Username = *username
	}
	return u, nil
}

// EnsureLinkToken возвращает deep-link токен аккаунта, создавая его при первом
// обращении. Токен живёт до тех пор, пока жив аккаунт: повторный клик по
// «Привязать Telegram» из того же браузера ведёт по той же ссылке.
func (s *Store) EnsureLinkToken(ctx context.Context, userID int64) (string, error) {
	var token *string
	if err := s.pool.QueryRow(ctx,
		`SELECT link_token FROM users WHERE id = $1`, userID).Scan(&token); err != nil {
		return "", err
	}
	if token != nil && *token != "" {
		return *token, nil
	}

	fresh, err := randomToken()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE users SET link_token = $1 WHERE id = $2`, fresh, userID); err != nil {
		return "", err
	}
	return fresh, nil
}

// LinkResult — что именно сделала LinkTelegramChat.
type LinkResult int

const (
	LinkedNew      LinkResult = iota // чат впервые привязан к аккаунту
	LinkedMerged                     // чат уже был привязан: сессии браузера перенесены на старый аккаунт
	LinkedSameUser                   // этот браузер уже привязан к этому чату — ничего не изменилось
)

// LinkTelegramChat привязывает Telegram-чат к аккаунту, которому принадлежит
// linkToken, и выставляет ему роль админа по isAdmin.
//
// Ключевой случай — LinkedMerged: человек открыл сайт в другом браузере, там
// завёлся новый анонимный аккаунт, и он жмёт «Привязать Telegram». Раньше бот
// отвечал «вы уже подписаны», а браузер навсегда оставался непривязанным — тот
// самый виджет у привязанного пользователя. Теперь сессии нового браузера
// переезжают на существующий аккаунт, а пустой анонимный аккаунт удаляется.
func (s *Store) LinkTelegramChat(ctx context.Context, linkToken string, chatID int64, username string, isAdmin bool) (LinkResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var tokenUserID int64
	err = tx.QueryRow(ctx,
		`SELECT id FROM users WHERE link_token = $1 FOR UPDATE`, linkToken).Scan(&tokenUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrLinkTokenNotFound
	}
	if err != nil {
		return 0, err
	}

	var chatUserID int64
	err = tx.QueryRow(ctx,
		`SELECT id FROM users WHERE telegram_chat_id = $1 FOR UPDATE`, chatID).Scan(&chatUserID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Чат ещё ничей — привязываем к аккаунту из ссылки.
		if _, err := tx.Exec(ctx,
			`UPDATE users SET telegram_chat_id = $1, telegram_username = $2, is_admin = $3 WHERE id = $4`,
			chatID, username, isAdmin, tokenUserID,
		); err != nil {
			return 0, err
		}
		return LinkedNew, tx.Commit(ctx)

	case err != nil:
		return 0, err

	case chatUserID == tokenUserID:
		// Тот же браузер, тот же чат: обновляем только имя и роль.
		if _, err := tx.Exec(ctx,
			`UPDATE users SET telegram_username = $1, is_admin = $2 WHERE id = $3`,
			username, isAdmin, tokenUserID,
		); err != nil {
			return 0, err
		}
		return LinkedSameUser, tx.Commit(ctx)
	}

	// Чат принадлежит другому (старому) аккаунту — переносим на него сессии
	// нового браузера и сносим опустевший анонимный аккаунт.
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET user_id = $1 WHERE user_id = $2`, chatUserID, tokenUserID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET telegram_username = $1, is_admin = $2 WHERE id = $3`,
		username, isAdmin, chatUserID,
	); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, tokenUserID); err != nil {
		return 0, err
	}
	return LinkedMerged, tx.Commit(ctx)
}

// UnlinkTelegramChat отвязывает чат от аккаунта (команда /stop). Сессии браузеров
// остаются живыми — сайт просто снова покажет «Привязать Telegram».
// Возвращает false, если чат не был привязан.
func (s *Store) UnlinkTelegramChat(ctx context.Context, chatID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET telegram_chat_id = NULL, is_admin = false WHERE telegram_chat_id = $1`,
		chatID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetAdminByChat пересчитывает роль уже привязанного чата — чтобы правка
// TELEGRAM_ADMINS применялась и без переподключения бота.
func (s *Store) SetAdminByChat(ctx context.Context, chatID int64, isAdmin bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET is_admin = $1 WHERE telegram_chat_id = $2`, isAdmin, chatID)
	return err
}
