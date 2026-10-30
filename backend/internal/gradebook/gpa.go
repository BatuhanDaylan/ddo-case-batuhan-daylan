// Package gradebook not girişi, ilan, düzeltme onayı ve GANO yeniden hesaplamasını içerir.
package gradebook

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/grading"
)

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// LoadGANO öğrencinin GANO'sunu kaynak veriden (course_results) hesaplar. Programın tekrar-alma
// politikasına göre ('last'|'best') ders başına tek deneme seçilir. Dönem sırası olarak term_id
// kullanılır (dönemler kronolojik sırayla açılır).
func LoadGANO(ctx context.Context, q queryer, studentID int64) (float64, error) {
	var policy string
	if err := q.QueryRow(ctx,
		`SELECT p.retake_policy FROM students s JOIN programs p ON p.id = s.program_id WHERE s.id = $1`,
		studentID).Scan(&policy); err != nil {
		return 0, err
	}
	rows, err := q.Query(ctx,
		`SELECT r.course_id, r.term_id, c.ects, r.grade_point::float8, r.exempt
		   FROM course_results r JOIN courses c ON c.id = r.course_id
		  WHERE r.student_id = $1`, studentID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var attempts []grading.Attempt
	for rows.Next() {
		var a grading.Attempt
		if err := rows.Scan(&a.CourseID, &a.TermOrder, &a.ECTS, &a.Point, &a.Exempt); err != nil {
			return 0, err
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return grading.Gano(attempts, policy), nil
}

// Enqueue öğrencilerin ortalamasını "eskimiş" olarak kuyruğa yazar. ON CONFLICT DO UPDATE kullanılır:
// işçi aynı satırı işlerken yeni bir değişiklik gelirse, işçi bitince satır yeniden oluşur ve
// değişiklik kaybolmaz (DO NOTHING bu durumda eski hesaplamayı kalıcı bırakırdı).
func Enqueue(ctx context.Context, ex execer, studentIDs []int64) error {
	if len(studentIDs) == 0 {
		return nil
	}
	_, err := ex.Exec(ctx,
		`INSERT INTO gano_jobs (student_id) SELECT unnest($1::bigint[])
		 ON CONFLICT (student_id) DO UPDATE SET enqueued_at = now()`, studentIDs)
	return err
}

// ProcessBatch kuyruktan en fazla limit öğrenci alıp GANO'larını yeniden hesaplar ve işlenen
// sayıyı döner. SKIP LOCKED sayesinde birden çok işçi aynı satırı işlemez. İş idempotenttir:
// sonuç yalnızca kaynak veriye bağlıdır, aynı iş tekrar çalışsa aynı değeri yazar.
func ProcessBatch(ctx context.Context, pool *pgxpool.Pool, limit int) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT student_id FROM gano_jobs ORDER BY enqueued_at LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, id := range ids {
		g, err := LoadGANO(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE students SET gano = $2, gano_updated_at = now() WHERE id = $1`, id, g); err != nil {
			return 0, err
		}
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM gano_jobs WHERE student_id = ANY($1)`, ids); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit(ctx)
}

// RunWorker, ctx iptal edilene kadar kuyruğu düzenli aralıklarla boşaltır.
func RunWorker(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for { // kuyruk boşalana kadar art arda işle
			n, err := ProcessBatch(ctx, pool, 200)
			if err != nil {
				if ctx.Err() == nil {
					log.Error("gano işçisi", "err", err)
				}
				break
			}
			if n == 0 {
				break
			}
			log.Info("gano yeniden hesaplandı", "ogrenci", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
