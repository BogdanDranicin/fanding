package storage

import "github.com/jackc/pgx/v5/pgxpool"

// Store provides read/write access to the PostgreSQL database.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps an existing connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}
