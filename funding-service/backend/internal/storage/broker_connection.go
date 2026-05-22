package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// BrokerConnection хранит учётные данные tradersdiaries.com для автообновления токена.
type BrokerConnection struct {
	SSOSession string
	DeviceID   string
	ExpiresAt  time.Time
}

// GetBrokerConnection возвращает единственную запись подключения.
// Возвращает (nil, nil) если запись не найдена.
func (s *Store) GetBrokerConnection(ctx context.Context) (*BrokerConnection, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT sso_session, device_id, expires_at FROM broker_connection ORDER BY id DESC LIMIT 1`)

	var c BrokerConnection
	if err := row.Scan(&c.SSOSession, &c.DeviceID, &c.ExpiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// UpsertBrokerConnection заменяет единственную запись (truncate + insert).
func (s *Store) UpsertBrokerConnection(ctx context.Context, conn BrokerConnection) error {
	_, err := s.pool.Exec(ctx,
		`TRUNCATE broker_connection;
		 INSERT INTO broker_connection (sso_session, device_id, expires_at)
		 VALUES ($1, $2, $3)`,
		conn.SSOSession, conn.DeviceID, conn.ExpiresAt)
	return err
}
