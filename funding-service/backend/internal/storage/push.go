package storage

import (
	"context"
	"fmt"
	"time"
)

// Подписки браузеров на push и очередь будильников.
//
// Очередь наполняет сама страница: расписание сигналов живёт в браузере, и
// сервер о нём знать не обязан — ему достаточно списка ближайших отметок. Пока
// страница открыта, она перекладывает список заново; закрытая страница
// доигрывает то, что успела положить.

// PushSubscription — подписка одного браузера.
type PushSubscription struct {
	ID       int64
	Endpoint string
	P256dh   string
	Auth     string
	// Funding — слать ли уведомление о публикации курса ЦБ. Сигналы по времени
	// приходят по расписанию из очереди и от этой галочки не зависят.
	Funding bool
}

// PushWakeup — одна отметка расписания: когда разбудить и что показать.
type PushWakeup struct {
	FireAt time.Time
	Title  string
	Body   string
	// Tag — по нему браузер схлопывает повторы: уведомление с тем же тегом
	// заменяет предыдущее, а не встаёт под ним третьим за час.
	Tag string
}

// DuePushWakeup — готовая к отправке строка очереди вместе с адресом браузера.
type DuePushWakeup struct {
	ID           int64
	Subscription PushSubscription
	Wakeup       PushWakeup
}

// SavePushSubscription запоминает подписку браузера. Один и тот же адрес
// браузер отдаёт снова после перезагрузки страницы — строка обновляется, а не
// плодится, и очередь, привязанная к ней, при этом не теряется.
func (s *Store) SavePushSubscription(ctx context.Context, session string, sub PushSubscription) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO push_subscriptions (session, endpoint, p256dh, auth, funding)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (endpoint) DO UPDATE
		SET session = EXCLUDED.session,
		    p256dh  = EXCLUDED.p256dh,
		    auth    = EXCLUDED.auth,
		    funding = EXCLUDED.funding,
		    seen_at = now()
		RETURNING id`,
		session, sub.Endpoint, sub.P256dh, sub.Auth, sub.Funding).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save push subscription: %w", err)
	}
	return id, nil
}

// DeletePushSubscription убирает подписку вместе с её очередью (каскадом).
func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint = $1`, endpoint)
	return err
}

// ReplacePushWakeups кладёт новое расписание браузера вместо старого.
//
// Именно заменяет, а не дополняет: страница присылает ближайшие отметки целиком,
// и отметка, которую пользователь только что удалил или сдвинул, обязана исчезнуть
// из очереди. Всё в одной транзакции — иначе между удалением и вставкой
// нашлось бы окно, в котором браузер остался бы без будильников вовсе.
func (s *Store) ReplacePushWakeups(ctx context.Context, subID int64, wakeups []PushWakeup) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM push_wakeups WHERE subscription = $1`, subID); err != nil {
		return fmt.Errorf("clear wakeups: %w", err)
	}
	for _, w := range wakeups {
		if _, err := tx.Exec(ctx, `
			INSERT INTO push_wakeups (subscription, fire_at, title, body, tag)
			VALUES ($1, $2, $3, $4, $5)`,
			subID, w.FireAt, w.Title, w.Body, w.Tag); err != nil {
			return fmt.Errorf("insert wakeup: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// AddPushWakeup ставит в очередь одну отметку, не трогая остальные. Нужен для
// проверки из настроек: пользователь жмёт кнопку, сворачивает окно и через
// несколько секунд видит, дошло ли уведомление.
func (s *Store) AddPushWakeup(ctx context.Context, subID int64, w PushWakeup) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO push_wakeups (subscription, fire_at, title, body, tag)
		VALUES ($1, $2, $3, $4, $5)`, subID, w.FireAt, w.Title, w.Body, w.Tag)
	return err
}

// DuePushWakeups забирает из очереди всё, чему пора, и сразу удаляет забранное.
//
// Забирает и удаляет одним запросом намеренно: диспетчер может оказаться не
// один (перезапуск с перекрытием, вторая копия сервиса), и строка, прочитанная
// дважды, обернулась бы двумя уведомлениями об одном сигнале.
//
// stale — насколько просроченную отметку ещё имеет смысл доставлять. Всё, что
// старше, выбрасывается молча: сигнал «через три секунды удар», доехавший через
// полчаса, — не напоминание, а мусор.
func (s *Store) DuePushWakeups(ctx context.Context, now time.Time, stale time.Duration, limit int) ([]DuePushWakeup, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			DELETE FROM push_wakeups
			WHERE id IN (
				SELECT id FROM push_wakeups
				WHERE fire_at <= $1
				ORDER BY fire_at
				LIMIT $3
			)
			RETURNING id, subscription, fire_at, title, body, tag
		)
		SELECT due.id, due.fire_at, due.title, due.body, due.tag,
		       s.id, s.endpoint, s.p256dh, s.auth
		FROM due
		JOIN push_subscriptions s ON s.id = due.subscription
		WHERE due.fire_at >= $2
		ORDER BY due.fire_at`,
		now, now.Add(-stale), limit)
	if err != nil {
		return nil, fmt.Errorf("due wakeups: %w", err)
	}
	defer rows.Close()

	var out []DuePushWakeup
	for rows.Next() {
		var d DuePushWakeup
		if err := rows.Scan(&d.ID, &d.Wakeup.FireAt, &d.Wakeup.Title, &d.Wakeup.Body, &d.Wakeup.Tag,
			&d.Subscription.ID, &d.Subscription.Endpoint, &d.Subscription.P256dh, &d.Subscription.Auth); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FundingPushSubscriptions — все браузеры, ждущие уведомления о публикации
// курса ЦБ. Их немного (по числу вкладок, а не пользователей), и список нужен
// раз в сутки — на публикации.
func (s *Store) FundingPushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, endpoint, p256dh, auth, funding
		FROM push_subscriptions
		WHERE funding`)
	if err != nil {
		return nil, fmt.Errorf("funding subscriptions: %w", err)
	}
	defer rows.Close()

	var out []PushSubscription
	for rows.Next() {
		var sub PushSubscription
		if err := rows.Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Funding); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// PushSubscriptionByEndpoint находит подписку сессии по её адресу. Чужая
// подписка не отдаётся: адрес — это ключ от уведомлений браузера.
func (s *Store) PushSubscriptionByEndpoint(ctx context.Context, session, endpoint string) (PushSubscription, error) {
	var sub PushSubscription
	err := s.pool.QueryRow(ctx, `
		SELECT id, endpoint, p256dh, auth, funding
		FROM push_subscriptions
		WHERE endpoint = $1 AND session = $2`, endpoint, session).
		Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Funding)
	return sub, err
}
