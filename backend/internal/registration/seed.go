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
	// Ders saatleri (gün 1=Pzt; dakika = saat*60). BM101/MAT101 Pazartesi günü bilerek çakışır,
	// BM104/BM105 de Cuma günü: çakışma kuralını elle denemek için.
	_, err = tx.Exec(ctx,
		`INSERT INTO section_meetings (section_id, day, start_min, end_min)
		 SELECT s.id, v.day, v.s, v.e
		   FROM sections s
		   JOIN courses c ON c.id = s.course_id
		   JOIN (VALUES ('BM101',1,540,660), ('MAT101',1,600,720), ('FIZ101',2,540,660),
		                ('BM102',3,540,660), ('MAT102',4,540,660), ('BM104',5,540,660),
		                ('BM105',5,600,720), ('BM301',4,780,900)) AS v(code, day, s, e) ON v.code = c.code
		  WHERE s.term_id = $1`, termID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
