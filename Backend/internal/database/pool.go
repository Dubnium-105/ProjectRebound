package database

import (
	"context"
	"fmt"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool struct {
	*pgxpool.Pool
}

func Open(ctx context.Context, cfg config.DBConfig) (*Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	poolConfig.MaxConns = cfg.MaxConnections
	poolConfig.MinConns = cfg.MinConnections
	poolConfig.MaxConnLifetime = cfg.MaxConnectionLifetime()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	wrapped := &Pool{Pool: pool}
	if err := wrapped.Check(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return wrapped, nil
}

func (p *Pool) Check(ctx context.Context) error {
	if err := p.Ping(ctx); err != nil {
		return fmt.Errorf("PostgreSQL ping: %w", err)
	}
	return nil
}
