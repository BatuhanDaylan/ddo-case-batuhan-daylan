// Package db bağlantı havuzunu ve gömülü migrasyonları yönetir.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Connect havuzu açar ve veritabanına ulaşılana kadar kısa süre yeniden dener.
//
// maxConns havuzun en fazla bağlantı sayısıdır (0 ise pgx varsayılanı: işlemci sayısı kadar, en az 4).
// Varsayılan çok küçüktür; kayıt anı gibi eşzamanlı yükte istekler havuzda kuyruğa girer. Değer,
// PostgreSQL'in max_connections sınırının (varsayılan 100) altında kalmalıdır.
func Connect(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("bağlantı adresi: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("havuz: %w", err)
	}
	var lastErr error
	for i := 0; i < 10; i++ {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		lastErr = pool.Ping(pctx)
		cancel()
		if lastErr == nil {
			return pool, nil
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	pool.Close()
	return nil, fmt.Errorf("veritabanına ulaşılamadı: %w", lastErr)
}

// Migrate gömülü goose migrasyonlarını uygular.
func Migrate(pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func(d *sql.DB) { _ = d.Close() }(sqlDB)
	return goose.Up(sqlDB, "migrations")
}
