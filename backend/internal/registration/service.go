// Package registration ders kaydı, kontenjan ve bekleme listesini yönetir.
package registration

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/audit"
)

var (
	ErrSectionNotFound    = errors.New("şube bulunamadı")
	ErrRegistrationClosed = errors.New("bu dönem için kayıt kapalı")
	ErrAlreadyRegistered  = errors.New("öğrenci bu şubeye zaten kayıtlı veya bekleme listesinde")
	ErrEnrollmentNotFound = errors.New("kayıt bulunamadı")
)

const (
	StatusEnrolled   = "enrolled"
	StatusWaitlisted = "waitlisted"
	StatusDropped    = "dropped"
)

type EnrollResult struct {
	EnrollmentID     int64  `json:"enrollment_id"`
	Status           string `json:"status"`
	WaitlistPosition *int   `json:"waitlist_position,omitempty"`
}

type DropResult struct {
	Status               string `json:"status"`
	PromotedEnrollmentID *int64 `json:"promoted_enrollment_id,omitempty"`
}

// EnrollTx öğrenciyi şubeye yazar; kontenjan doluysa bekleme listesine koyar.
//
// Yarış durumu çözümü: şube satırı SELECT ... FOR UPDATE ile kilitlenir. Aynı şubeye gelen
// tüm kayıt/bırakma işlemleri bu satır kilidinde sıraya girer; böylece
//   - "kontenjan var mı" kontrolü ile "sayacı artır" adımı arasına kimse giremez (aşım yok),
//   - enrollments.id'ler kilit alma sırasıyla verilir (bekleme listesi adil sıradadır).
//
// Farklı şubeler birbirini beklemez. Kilit, çağıranın transaction'ı bitince bırakılır.
func EnrollTx(ctx context.Context, tx pgx.Tx, studentID, sectionID int64) (EnrollResult, error) {
	var capacity, enrolled int
	var open bool
	err := tx.QueryRow(ctx,
		`SELECT s.capacity, s.enrolled_count, t.registration_open
		   FROM sections s JOIN terms t ON t.id = s.term_id
		  WHERE s.id = $1 FOR UPDATE OF s`, sectionID).Scan(&capacity, &enrolled, &open)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnrollResult{}, ErrSectionNotFound
	}
	if err != nil {
		return EnrollResult{}, err
	}
	if !open {
		return EnrollResult{}, ErrRegistrationClosed
	}

	var already bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM enrollments
		                 WHERE section_id = $1 AND student_id = $2 AND status IN ('enrolled', 'waitlisted'))`,
		sectionID, studentID).Scan(&already); err != nil {
		return EnrollResult{}, err
	}
	if already {
		return EnrollResult{}, ErrAlreadyRegistered
	}

	status := StatusWaitlisted
	if enrolled < capacity {
		status = StatusEnrolled
		if _, err := tx.Exec(ctx,
			`UPDATE sections SET enrolled_count = enrolled_count + 1 WHERE id = $1`, sectionID); err != nil {
			return EnrollResult{}, err
		}
	}

	res := EnrollResult{Status: status}
	if err := tx.QueryRow(ctx,
		`INSERT INTO enrollments (section_id, student_id, status) VALUES ($1, $2, $3) RETURNING id`,
		sectionID, studentID, status).Scan(&res.EnrollmentID); err != nil {
		return EnrollResult{}, err
	}
	if status == StatusWaitlisted {
		var pos int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM enrollments WHERE section_id = $1 AND status = 'waitlisted' AND id <= $2`,
			sectionID, res.EnrollmentID).Scan(&pos); err != nil {
			return EnrollResult{}, err
		}
		res.WaitlistPosition = &pos
	}

	err = audit.Write(ctx, tx, audit.Entry{
		Action: "enrollment." + status, Entity: "enrollment", EntityID: strconv.FormatInt(res.EnrollmentID, 10),
		New: map[string]any{"section_id": sectionID, "student_id": studentID, "status": status},
	})
	return res, err
}

// DropTx kaydı bırakır. Boşalan koltuk varsa bekleme listesinin ilk öğrencisine
// AYNI transaction içinde otomatik atanır; arada koltuk "havada" kalmaz.
// Zaten bırakılmış kayıt için tekrar çağrı zararsızdır (idempotent).
func DropTx(ctx context.Context, tx pgx.Tx, studentID, enrollmentID int64) (DropResult, error) {
	var sectionID, owner int64
	err := tx.QueryRow(ctx,
		`SELECT section_id, student_id FROM enrollments WHERE id = $1`, enrollmentID).Scan(&sectionID, &owner)
	// Başkasının kaydı için "yok" deriz: kayıt kimliklerinin varlığı sızmasın.
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != studentID) {
		return DropResult{}, ErrEnrollmentNotFound
	}
	if err != nil {
		return DropResult{}, err
	}

	// Kilit sırası her yerde aynı: önce şube, sonra kayıt satırları (kilit kilitlenmesi olmaz).
	if _, err := tx.Exec(ctx, `SELECT 1 FROM sections WHERE id = $1 FOR UPDATE`, sectionID); err != nil {
		return DropResult{}, err
	}
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM enrollments WHERE id = $1 FOR UPDATE`, enrollmentID).Scan(&status); err != nil {
		return DropResult{}, err
	}
	if status == StatusDropped {
		return DropResult{Status: StatusDropped}, nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE enrollments SET status = 'dropped', dropped_at = now() WHERE id = $1`, enrollmentID); err != nil {
		return DropResult{}, err
	}

	res := DropResult{Status: StatusDropped}
	if status == StatusEnrolled {
		var next int64
		err := tx.QueryRow(ctx,
			`SELECT id FROM enrollments WHERE section_id = $1 AND status = 'waitlisted'
			  ORDER BY id LIMIT 1 FOR UPDATE`, sectionID).Scan(&next)
		switch {
		case err == nil:
			// Koltuk el değiştirir; enrolled_count aynı kalır.
			if _, err := tx.Exec(ctx,
				`UPDATE enrollments SET status = 'enrolled', promoted_at = now() WHERE id = $1`, next); err != nil {
				return DropResult{}, err
			}
			res.PromotedEnrollmentID = &next
			if err := audit.Write(ctx, tx, audit.Entry{
				Action: "enrollment.promoted", Entity: "enrollment", EntityID: strconv.FormatInt(next, 10),
				Old: map[string]string{"status": StatusWaitlisted}, New: map[string]string{"status": StatusEnrolled},
			}); err != nil {
				return DropResult{}, err
			}
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx,
				`UPDATE sections SET enrolled_count = enrolled_count - 1 WHERE id = $1`, sectionID); err != nil {
				return DropResult{}, err
			}
		default:
			return DropResult{}, err
		}
	}

	err = audit.Write(ctx, tx, audit.Entry{
		Action: "enrollment.dropped", Entity: "enrollment", EntityID: strconv.FormatInt(enrollmentID, 10),
		Old: map[string]string{"status": status}, New: map[string]string{"status": StatusDropped},
	})
	return res, err
}
