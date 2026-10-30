package registration

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedDemo açık bir dönem ve seed derslerinin her biri için bir şube kurar.
// BM301 şubesi bilerek 2 kişilik: bekleme listesi demosu için. İdempotenttir.
// Önce auth.SeedDemoUsers ve academic.SeedDemo çalışmış olmalı.
func SeedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM terms)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var termID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO terms (code, name, registration_open) VALUES ('2026-GUZ', '2026-2027 Güz', true) RETURNING id`).
		Scan(&termID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO sections (term_id, course_id, code, instructor_id, capacity)
		 SELECT $1, c.id, '01', (SELECT id FROM users WHERE email = 'hoca@uninet.test'),
		        CASE WHEN c.code = 'BM301' THEN 2 ELSE 40 END
		   FROM courses c ORDER BY c.id`, termID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
