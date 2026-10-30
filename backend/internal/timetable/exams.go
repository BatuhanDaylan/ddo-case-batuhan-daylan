package timetable

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

// ExamRoutes sınav takvimi uç noktalarını bağlar (Authenticate arkasında).
func (h *Handler) ExamRoutes(r chi.Router) {
	admins := auth.Require(auth.RoleRegistrar, auth.RoleDeptSecretary)
	r.With(admins).Post("/sections/{id}/exams", h.createExam)
	r.With(admins).Delete("/exams/{id}", h.deleteExam)
	r.Get("/terms/{id}/exams", h.listExams)
	r.With(auth.Require(auth.RoleStudent)).Get("/me/exam-schedule", h.myExams)
}

type examRow struct {
	ID          int64     `json:"id"`
	SectionID   int64     `json:"section_id"`
	CourseCode  string    `json:"course_code"`
	Kind        string    `json:"kind"`
	StartsAt    time.Time `json:"starts_at"`
	DurationMin int       `json:"duration_min"`
	Room        string    `json:"room,omitempty"`
}

// examConflict bir sınavın neden yerleştirilemediğini açıklar.
type examConflict struct {
	Kind    string `json:"kind"` // ROOM | INSTRUCTOR | STUDENT
	Message string `json:"message"`
}

// createExam: POST /sections/{id}/exams {"kind":"midterm|final|makeup","starts_at":"…","duration_min":90,"room":"SINIF-101"}
//
// Üç çakışma türü REDDEDİLİR (409, hepsi birlikte listelenir): aynı derslikte zaman çakışması, aynı öğretim
// üyesinin iki sınavı, ve aynı öğrencinin (aktif kaydı olan) iki sınavı. Kontrol ve ekleme aynı transaction'da
// ve dönem kilidi altında yapılır; iki yönetici eşzamanlı çakışan sınav girerse biri reddedilir.
func (h *Handler) createExam(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Kind        string    `json:"kind"`
		StartsAt    time.Time `json:"starts_at"`
		DurationMin int       `json:"duration_min"`
		Room        string    `json:"room"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"midterm", "final", "makeup"}, in.Kind) || in.StartsAt.IsZero() ||
		in.DurationMin < 15 || in.DurationMin > 300 {
		httpx.Error(w, http.StatusBadRequest, "validation_error",
			"kind (midterm|final|makeup), starts_at ve 15-300 arası duration_min zorunlu")
		return
	}
	ends := in.StartsAt.Add(time.Duration(in.DurationMin) * time.Minute)

	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var termID int64
	var instructor *int64
	var code string
	err = tx.QueryRow(ctx,
		`SELECT s.term_id, s.instructor_id, c.code FROM sections s JOIN courses c ON c.id = s.course_id WHERE s.id = $1`,
		sectionID).Scan(&termID, &instructor, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "şube bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	// Dönem bazlı danışma kilidi: eşzamanlı iki sınav girişi birbirini göremeden çakışmasın.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7002, $1::int)`, termID); err != nil {
		httpx.Internal(w, err)
		return
	}

	var roomID *int64
	if in.Room != "" {
		var rid int64
		if err := tx.QueryRow(ctx, `SELECT id FROM rooms WHERE code = $1`, in.Room).Scan(&rid); errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid_reference", "derslik bulunamadı")
			return
		} else if err != nil {
			httpx.Internal(w, err)
			return
		}
		roomID = &rid
	}

	// Sınav aralığı [starts_at, starts_at + duration) ile çakışan diğer sınavlar (yarı açık aralık).
	const overlap = `e.starts_at < $2 AND e.starts_at + make_interval(mins => e.duration_min) > $1 AND e.section_id <> $3`
	var conflicts []examConflict
	add := func(kind, sql string, args ...any) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var other string
			var extra *int
			if err := rows.Scan(&other, &extra); err != nil {
				return err
			}
			msg := fmt.Sprintf("%s sınavıyla çakışıyor", other)
			if extra != nil {
				msg = fmt.Sprintf("%s sınavıyla %d öğrencide çakışıyor", other, *extra)
			}
			conflicts = append(conflicts, examConflict{Kind: kind, Message: msg})
		}
		return rows.Err()
	}
	if roomID != nil {
		if err := add("ROOM", `SELECT c.code, NULL::int FROM exams e JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id
			WHERE e.room_id = $4 AND `+overlap, in.StartsAt, ends, sectionID, *roomID); err != nil {
			httpx.Internal(w, err)
			return
		}
	}
	if instructor != nil {
		if err := add("INSTRUCTOR", `SELECT c.code, NULL::int FROM exams e JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id
			WHERE s.instructor_id = $4 AND `+overlap, in.StartsAt, ends, sectionID, *instructor); err != nil {
			httpx.Internal(w, err)
			return
		}
	}
	if err := add("STUDENT", `SELECT c.code, count(DISTINCT mine.student_id)::int
		FROM exams e JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id
		JOIN enrollments other ON other.section_id = s.id AND other.status = 'enrolled'
		JOIN enrollments mine ON mine.student_id = other.student_id AND mine.section_id = $4 AND mine.status = 'enrolled'
		WHERE `+overlap+` GROUP BY c.code`, in.StartsAt, ends, sectionID, sectionID); err != nil {
		httpx.Internal(w, err)
		return
	}
	if len(conflicts) > 0 {
		httpx.JSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
			"code": "exam_conflict", "message": code + " sınavı yerleştirilemedi: çakışma var", "conflicts": conflicts}})
		return
	}

	var id int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO exams (section_id, kind, starts_at, duration_min, room_id) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		sectionID, in.Kind, in.StartsAt, in.DurationMin, roomID).Scan(&id); err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "exam.create", Entity: "exam",
		EntityID: strconv.FormatInt(id, 10), New: in, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, examRow{ID: id, SectionID: sectionID, CourseCode: code, Kind: in.Kind,
		StartsAt: in.StartsAt, DurationMin: in.DurationMin, Room: in.Room})
}

func (h *Handler) deleteExam(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sectionID int64
	if err := tx.QueryRow(ctx, `DELETE FROM exams WHERE id = $1 RETURNING section_id`, id).Scan(&sectionID); errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "sınav bulunamadı")
		return
	} else if err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "exam.delete", Entity: "exam",
		EntityID: strconv.FormatInt(id, 10), Old: map[string]int64{"section_id": sectionID}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const examSelect = `SELECT e.id, e.section_id, c.code, e.kind, e.starts_at, e.duration_min, COALESCE(rm.code, '')
	FROM exams e JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id LEFT JOIN rooms rm ON rm.id = e.room_id`

func scanExams(rows pgx.Rows) ([]examRow, error) {
	defer rows.Close()
	var out []examRow
	for rows.Next() {
		var x examRow
		if err := rows.Scan(&x.ID, &x.SectionID, &x.CourseCode, &x.Kind, &x.StartsAt, &x.DurationMin, &x.Room); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// listExams: GET /terms/{id}/exams — dönemin sınav takvimi (keyset sayfalı).
func (h *Handler) listExams(w http.ResponseWriter, r *http.Request) {
	termID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	rows, err := h.pool.Query(r.Context(), examSelect+` WHERE s.term_id = $1 AND e.id > $2 ORDER BY e.id LIMIT $3`, termID, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	items, err := scanExams(rows)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x examRow) int64 { return x.ID }))
}

// myExams: GET /me/exam-schedule — öğrencinin aktif kayıtlı olduğu şubelerin sınavları, zamana göre sıralı.
func (h *Handler) myExams(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), examSelect+`
		JOIN enrollments en ON en.section_id = s.id AND en.status = 'enrolled'
		JOIN students st ON st.id = en.student_id AND st.user_id = $1
		ORDER BY e.starts_at, e.id`, p.UserID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	items, err := scanExams(rows)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if items == nil {
		items = []examRow{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
