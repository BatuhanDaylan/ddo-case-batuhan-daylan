package simulator

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAlreadySeeded simülasyon verisi zaten yüklüyse döner (yükleme idempotenttir, üzerine yazmaz).
var ErrAlreadySeeded = errors.New("simülasyon verisi zaten yüklü (sıfırlamak için: make reset-demo)")

// Load veri setini tek transaction'da PostgreSQL COPY ile yükler. Kimlikler açıkça verildiği için
// ardından kimlik sıraları (sequence) en büyük değere ayarlanır; böylece sonraki normal eklemeler çakışmaz.
// Yükleme sonunda tüm öğrencilerin GANO'su arka plan kuyruğuna alınır.
func Load(ctx context.Context, pool *pgxpool.Pool, d Dataset) error {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM faculties WHERE code = 'SIM-F1')`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrAlreadySeeded
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, t := range d.Tables {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{t.Name}, t.Cols, pgx.CopyFromRows(t.Rows)); err != nil {
			return fmt.Errorf("%s yüklenemedi: %w", t.Name, err)
		}
	}
	for _, t := range d.Tables {
		if t.Cols[0] != "id" {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s', 'id'), (SELECT max(id) FROM %s))`, t.Name, t.Name)); err != nil {
			return fmt.Errorf("%s sırası ayarlanamadı: %w", t.Name, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO gano_jobs (student_id) SELECT id FROM students WHERE student_no LIKE 'SIM%' ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Violation kota doğrulamasında bulunan bir ihlaldir.
type Violation struct {
	Check string
	Count int
}

// CheckQuota kayıt anı yükünden sonra veritabanının tutarlılığını denetler. Dönen liste boşsa kota aşımı yoktur.
func CheckQuota(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error) {
	checks := []struct{ name, sql string }{
		{"enrolled_count kontenjanı aşıyor",
			`SELECT count(*) FROM sections WHERE enrolled_count > capacity`},
		{"gerçek 'enrolled' satır sayısı kontenjanı aşıyor",
			`SELECT count(*) FROM (SELECT s.id FROM sections s JOIN enrollments e ON e.section_id = s.id AND e.status = 'enrolled'
			   GROUP BY s.id, s.capacity HAVING count(*) > s.capacity) x`},
		{"enrolled_count gerçek satır sayısıyla tutarsız",
			`SELECT count(*) FROM sections s WHERE s.enrolled_count <>
			   (SELECT count(*) FROM enrollments e WHERE e.section_id = s.id AND e.status = 'enrolled')`},
		{"aynı öğrenci aynı şubede birden çok aktif kayıtlı",
			`SELECT count(*) FROM (SELECT 1 FROM enrollments WHERE status IN ('enrolled','waitlisted')
			   GROUP BY section_id, student_id HAVING count(*) > 1) x`},
		{"sıra ihlali: bekleyen, kendinden sonra gelen biri koltuk almışken kalmış",
			`SELECT count(*) FROM enrollments w JOIN enrollments e ON e.section_id = w.section_id
			   WHERE w.status = 'waitlisted' AND e.status = 'enrolled' AND e.id > w.id AND e.promoted_at IS NULL`},
		{"şube dolu değilken bekleyen var (boş koltuk bekleme listesine atanmamış)",
			`SELECT count(*) FROM sections s WHERE s.enrolled_count < s.capacity
			   AND EXISTS (SELECT 1 FROM enrollments e WHERE e.section_id = s.id AND e.status = 'waitlisted')`},
	}
	var out []Violation
	for _, c := range checks {
		var n int
		if err := pool.QueryRow(ctx, c.sql).Scan(&n); err != nil {
			return nil, fmt.Errorf("%s: %w", c.name, err)
		}
		if n > 0 {
			out = append(out, Violation{Check: c.name, Count: n})
		}
	}
	return out, nil
}
