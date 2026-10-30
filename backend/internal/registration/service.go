// Package registration ders kaydı, kontenjan ve bekleme listesini yönetir.
package registration

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/rules"
)

var tracer = otel.Tracer("uninet/registration")

var (
	ErrSectionNotFound    = errors.New("şube bulunamadı")
	ErrRegistrationClosed = errors.New("bu dönem için kayıt kapalı")
	ErrAlreadyRegistered  = errors.New("öğrenci bu şubeye zaten kayıtlı veya bekleme listesinde")
	ErrEnrollmentNotFound = errors.New("kayıt bulunamadı")
	ErrDropPeriodOver     = errors.New("ekle-sil süresi sona erdi")
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
//
// Sıra: (1) öğrenci satırı kilidi, (2) kural denetimi (T-2), (3) şube satırı kilidi.
// Kurallar şube kilidinden ÖNCE değerlendirilir: ağır okumalar kritik bölümü uzatmaz.
// Öğrenci kilidi, aynı öğrencinin iki şubeye eşzamanlı kaydolarak AKTS limitini veya çakışma
// kuralını aşmasını önler. DropTx öğrenci kilidi almadığı için kilit sırası döngü oluşturmaz.
//
// İzleme (OpenTelemetry): akış "registration.enroll" kök span'i ve her aşama için bir alt span üretir
// (öğrenci kilidi, kural yükleme/denetimi, şube kilidi, kayıt ekleme, denetim). Kilit beklemesi
// db.lock_section süresinde görünür; iş sonuçları (reddedilen kural vb.) hata değil, öznitelik olarak işlenir.
func EnrollTx(ctx context.Context, tx pgx.Tx, studentID, sectionID int64) (EnrollResult, error) {
	ctx, root := tracer.Start(ctx, "registration.enroll", trace.WithAttributes(
		attribute.Int64("student.id", studentID), attribute.Int64("section.id", sectionID)))
	defer root.End()

	res, outcome, err := enrollSteps(ctx, tx, studentID, sectionID)
	root.SetAttributes(attribute.String("enrollment.outcome", outcome))
	if err != nil && outcome == "error" {
		root.RecordError(err)
		root.SetStatus(codes.Error, err.Error())
	}
	return res, err
}

// stage bir aşamayı alt span olarak sarar. Beklenmeyen hata span'e kaydedilir.
func stage[T any](ctx context.Context, name string, fn func(ctx context.Context) (T, error)) (T, error) {
	ctx, sp := tracer.Start(ctx, name)
	defer sp.End()
	v, err := fn(ctx)
	if err != nil && !isBusinessError(err) {
		sp.RecordError(err)
		sp.SetStatus(codes.Error, err.Error())
	}
	return v, err
}

// isBusinessError beklenen iş sonuçlarını (kayıt kapalı, zaten kayıtlı, kural ihlali...) ayırt eder.
func isBusinessError(err error) bool {
	var rv *RuleViolationsError
	return errors.Is(err, ErrRegistrationClosed) || errors.Is(err, ErrAlreadyRegistered) ||
		errors.Is(err, ErrSectionNotFound) || errors.As(err, &rv)
}

func enrollSteps(ctx context.Context, tx pgx.Tx, studentID, sectionID int64) (EnrollResult, string, error) {
	fail := func(err error) (EnrollResult, string, error) {
		switch {
		case isBusinessError(err):
			return EnrollResult{}, "rejected", err
		default:
			return EnrollResult{}, "error", err
		}
	}

	if _, err := stage(ctx, "db.lock_student", func(ctx context.Context) (struct{}, error) {
		_, err := tx.Exec(ctx, `SELECT 1 FROM students WHERE id = $1 FOR UPDATE`, studentID)
		return struct{}{}, err
	}); err != nil {
		return fail(err)
	}

	ld, err := stage(ctx, "rules.load_input", func(ctx context.Context) (loaded, error) {
		return loadInput(ctx, tx, studentID, []int64{sectionID})
	})
	if err != nil {
		return fail(err)
	}
	if !ld.TermOpen {
		return fail(ErrRegistrationClosed)
	}
	for _, c := range ld.Input.Current {
		if c.SectionID == sectionID {
			return fail(ErrAlreadyRegistered)
		}
	}
	if _, err := stage(ctx, "rules.check", func(context.Context) (struct{}, error) {
		if rep := rules.Check(ld.Input); !rep.OK() {
			return struct{}{}, &RuleViolationsError{Report: rep}
		}
		return struct{}{}, nil
	}); err != nil {
		return fail(err)
	}

	type decision struct {
		status string
	}
	dec, err := stage(ctx, "db.lock_section", func(ctx context.Context) (decision, error) {
		var capacity, enrolled int
		err := tx.QueryRow(ctx,
			`SELECT capacity, enrolled_count FROM sections WHERE id = $1 FOR UPDATE`, sectionID).Scan(&capacity, &enrolled)
		if errors.Is(err, pgx.ErrNoRows) {
			return decision{}, ErrSectionNotFound
		}
		if err != nil {
			return decision{}, err
		}
		if enrolled < capacity {
			if _, err := tx.Exec(ctx,
				`UPDATE sections SET enrolled_count = enrolled_count + 1 WHERE id = $1`, sectionID); err != nil {
				return decision{}, err
			}
			return decision{StatusEnrolled}, nil
		}
		return decision{StatusWaitlisted}, nil
	})
	if err != nil {
		return fail(err)
	}

	res, err := stage(ctx, "db.insert_enrollment", func(ctx context.Context) (EnrollResult, error) {
		res := EnrollResult{Status: dec.status}
		if err := tx.QueryRow(ctx,
			`INSERT INTO enrollments (section_id, student_id, status) VALUES ($1, $2, $3) RETURNING id`,
			sectionID, studentID, dec.status).Scan(&res.EnrollmentID); err != nil {
			return EnrollResult{}, err
		}
		if dec.status == StatusWaitlisted {
			var pos int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM enrollments WHERE section_id = $1 AND status = 'waitlisted' AND id <= $2`,
				sectionID, res.EnrollmentID).Scan(&pos); err != nil {
				return EnrollResult{}, err
			}
			res.WaitlistPosition = &pos
		}
		return res, nil
	})
	if err != nil {
		return fail(err)
	}

	if _, err := stage(ctx, "audit.write", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, audit.Write(ctx, tx, audit.Entry{
			ActorID: actorOf(ctx, tx, studentID), Action: "enrollment." + dec.status, Entity: "enrollment",
			EntityID: strconv.FormatInt(res.EnrollmentID, 10),
			New:      map[string]any{"section_id": sectionID, "student_id": studentID, "status": dec.status},
		})
	}); err != nil {
		return fail(err)
	}
	return res, dec.status, nil
}

// DropTx kaydı bırakır. Boşalan koltuk varsa bekleme listesinin ilk öğrencisine
// AYNI transaction içinde otomatik atanır; arada koltuk "havada" kalmaz.
// Zaten bırakılmış kayıt için tekrar çağrı zararsızdır (idempotent).
//
// Öğrencinin kendi bırakması ekle-sil süresine (terms.drop_deadline) tabidir.
func DropTx(ctx context.Context, tx pgx.Tx, studentID, enrollmentID int64) (DropResult, error) {
	return dropTx(ctx, tx, studentID, enrollmentID, true)
}

// dropTx ortak bırakma mantığıdır. enforceDeadline=false, danışman reddi gibi süreye tabi olmayan
// idari bırakmalar içindir.
func dropTx(ctx context.Context, tx pgx.Tx, studentID, enrollmentID int64, enforceDeadline bool) (DropResult, error) {
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
	if enforceDeadline {
		var over bool
		if err := tx.QueryRow(ctx,
			`SELECT t.drop_deadline IS NOT NULL AND now() > t.drop_deadline
			   FROM sections s JOIN terms t ON t.id = s.term_id WHERE s.id = $1`, sectionID).Scan(&over); err != nil {
			return DropResult{}, err
		}
		if over {
			return DropResult{}, ErrDropPeriodOver
		}
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
				ActorID: actorOf(ctx, tx, studentID), Action: "enrollment.promoted", Entity: "enrollment", EntityID: strconv.FormatInt(next, 10),
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
		ActorID: actorOf(ctx, tx, studentID), Action: "enrollment.dropped", Entity: "enrollment", EntityID: strconv.FormatInt(enrollmentID, 10),
		Old: map[string]string{"status": status}, New: map[string]string{"status": StatusDropped},
	})
	return res, err
}

// actorOf, öğrencinin kullanıcı kimliğini verir; denetim kaydında "kim yaptı" alanı için.
// Bulunamazsa nil döner (denetim yine yazılır, yalnızca aktör boş kalır).
func actorOf(ctx context.Context, tx pgx.Tx, studentID int64) *int64 {
	var uid int64
	if err := tx.QueryRow(ctx, `SELECT user_id FROM students WHERE id = $1`, studentID).Scan(&uid); err != nil {
		return nil
	}
	return &uid
}
