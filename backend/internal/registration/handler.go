package registration

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/idempotency"
	"github.com/batuhan-daylan/uninet/internal/metrics"
	"github.com/batuhan-daylan/uninet/internal/rules"
)

type Handler struct{ pool *pgxpool.Pool }

func NewHandler(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// Routes çağıranın Authenticate middleware'i arkasında bağlanmalıdır.
func (h *Handler) Routes(r chi.Router) {
	admins := auth.Require(auth.RoleRegistrar, auth.RoleDeptSecretary)
	student := auth.Require(auth.RoleStudent)

	r.Get("/terms", h.listTerms)
	r.With(admins).Post("/terms", h.createTerm)
	r.With(admins).Patch("/terms/{id}", h.setRegistration)

	r.Get("/sections", h.listSections)
	r.Get("/sections/{id}", h.getSection)
	r.With(admins).Post("/sections", h.createSection)

	r.With(student).Post("/sections/{id}/enrollments", h.enroll)
	r.With(student).Delete("/enrollments/{id}", h.drop)
	r.With(student).Get("/me/enrollments", h.myEnrollments)
	r.With(student).Post("/me/enrollments/validate", h.validate)

	r.With(admins).Post("/sections/{id}/meetings", h.addMeeting)
}

// validate: POST /me/enrollments/validate  {"section_ids":[…]}
// Önerilen şubeleri mevcut programla birlikte değerlendirir ve BÜTÜN ihlalleri tek yanıtta listeler.
// Salt okunurdur; hiçbir şey yazmaz.
func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SectionIDs []int64 `json:"section_ids"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if len(in.SectionIDs) == 0 || len(in.SectionIDs) > 20 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "section_ids 1-20 eleman içermeli")
		return
	}
	studentID, ok := h.studentID(w, r)
	if !ok {
		return
	}
	// REPEATABLE READ: birden çok sorgu aynı anlık görüntüyü görür; tutarsız rapor çıkmaz.
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	ld, err := loadInput(r.Context(), tx, studentID, in.SectionIDs)
	switch {
	case errors.Is(err, ErrSectionNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", err.Error())
		return
	case errors.Is(err, ErrMixedTerms):
		httpx.Error(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	case err != nil:
		httpx.Internal(w, err)
		return
	}
	rep := rules.Check(ld.Input)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"valid": rep.OK(), "violations": rep.Violations,
		"total_ects": rep.TotalECTS, "max_ects": rep.MaxECTS, "gano": ld.Input.GANO,
	})
}

func (h *Handler) addMeeting(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Day      int `json:"day"`
		StartMin int `json:"start_min"`
		EndMin   int `json:"end_min"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.Day < 1 || in.Day > 7 || in.StartMin < 0 || in.EndMin > 1440 || in.StartMin >= in.EndMin {
		httpx.Error(w, http.StatusBadRequest, "validation_error",
			"day 1-7 olmalı; 0 <= start_min < end_min <= 1440 olmalı")
		return
	}
	id, err := h.insertAudited(r, "section_meeting", map[string]int{"day": in.Day, "start_min": in.StartMin, "end_min": in.EndMin},
		func(tx pgx.Tx) (int64, error) {
			var id int64
			return id, tx.QueryRow(r.Context(),
				`INSERT INTO section_meetings (section_id, day, start_min, end_min) VALUES ($1, $2, $3, $4) RETURNING id`,
				sectionID, in.Day, in.StartMin, in.EndMin).Scan(&id)
		})
	if err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"id": id, "section_id": sectionID, "day": in.Day, "start_min": in.StartMin, "end_min": in.EndMin,
	})
}

// studentID oturumdaki kullanıcının öğrenci kaydını bulur.
func (h *Handler) studentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	p, _ := auth.PrincipalFrom(r.Context())
	var id int64
	err := h.pool.QueryRow(r.Context(), `SELECT id FROM students WHERE user_id = $1`, p.UserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusForbidden, "no_student_record", "bu kullanıcı için öğrenci kaydı yok")
		return 0, false
	}
	if err != nil {
		httpx.Internal(w, err)
		return 0, false
	}
	return id, true
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func errBody(code, msg string) []byte {
	return jsonBody(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// violationsBody ortak hata biçimini korur ve tüm ihlalleri "violations" altında listeler.
func violationsBody(rep rules.Report) []byte {
	return jsonBody(map[string]any{"error": map[string]any{
		"code":       "rule_violation",
		"message":    "kayıt kuralları ihlal edildi",
		"violations": rep.Violations,
		"total_ects": rep.TotalECTS,
		"max_ects":   rep.MaxECTS,
	}})
}

func writeRaw(w http.ResponseWriter, status int, body []byte, replayed bool) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// enroll: POST /sections/{id}/enrollments  (Idempotency-Key başlığı zorunlu)
func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 255 {
		httpx.Error(w, http.StatusBadRequest, "idempotency_key_required",
			"Idempotency-Key başlığı zorunlu (en fazla 255 karakter)")
		return
	}
	studentID, ok := h.studentID(w, r)
	if !ok {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())

	res, err := idempotency.Do(r.Context(), h.pool, p.UserID, key, "POST /sections/"+strconv.FormatInt(sectionID, 10)+"/enrollments",
		func(tx pgx.Tx) (int, []byte, error) {
			out, err := EnrollTx(r.Context(), tx, studentID, sectionID)
			switch {
			case err == nil:
				metrics.Enrollments.WithLabelValues(out.Status).Inc()
				return http.StatusCreated, jsonBody(out), nil
			case errors.Is(err, ErrSectionNotFound):
				return http.StatusNotFound, errBody("not_found", err.Error()), nil
			case errors.Is(err, ErrRegistrationClosed):
				metrics.Enrollments.WithLabelValues("registration_closed").Inc()
				return http.StatusConflict, errBody("registration_closed", err.Error()), nil
			case errors.Is(err, ErrAlreadyRegistered):
				metrics.Enrollments.WithLabelValues("already_registered").Inc()
				return http.StatusConflict, errBody("already_registered", err.Error()), nil
			default:
				var rv *RuleViolationsError
				if errors.As(err, &rv) {
					metrics.Enrollments.WithLabelValues("rule_violation").Inc()
					return http.StatusUnprocessableEntity, violationsBody(rv.Report), nil
				}
				return 0, nil, err
			}
		})
	if errors.Is(err, idempotency.ErrKeyReuse) {
		httpx.Error(w, http.StatusUnprocessableEntity, "idempotency_key_reuse",
			"bu Idempotency-Key farklı bir istek için zaten kullanıldı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	writeRaw(w, res.Status, res.Body, res.Replayed)
}

// drop: DELETE /enrollments/{id}
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) {
	enrollmentID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	studentID, ok := h.studentID(w, r)
	if !ok {
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	out, err := DropTx(r.Context(), tx, studentID, enrollmentID)
	if errors.Is(err, ErrEnrollmentNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if errors.Is(err, ErrDropPeriodOver) {
		httpx.Error(w, http.StatusConflict, "drop_period_over", "ekle-sil süresi sona erdi; kaydı yalnızca danışman/öğrenci işleri kaldırabilir")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type enrollmentRow struct {
	ID               int64  `json:"id"`
	SectionID        int64  `json:"section_id"`
	Status           string `json:"status"`
	CourseCode       string `json:"course_code"`
	CourseName       string `json:"course_name"`
	ECTS             int    `json:"ects"`
	AdvisorStatus    string `json:"advisor_status"`
	WaitlistPosition *int   `json:"waitlist_position,omitempty"`
}

func (h *Handler) myEnrollments(w http.ResponseWriter, r *http.Request) {
	studentID, ok := h.studentID(w, r)
	if !ok {
		return
	}
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT e.id, e.section_id, e.status, c.code, c.name, c.ects, e.advisor_status,
		        CASE WHEN e.status = 'waitlisted' THEN
		          (SELECT count(*)::int FROM enrollments w WHERE w.section_id = e.section_id AND w.status = 'waitlisted' AND w.id <= e.id)
		        END
		   FROM enrollments e JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id
		  WHERE e.student_id = $1 AND e.id > $2 AND e.status <> 'dropped' ORDER BY e.id LIMIT $3`,
		studentID, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	var items []enrollmentRow
	for rows.Next() {
		var e enrollmentRow
		if err := rows.Scan(&e.ID, &e.SectionID, &e.Status, &e.CourseCode, &e.CourseName, &e.ECTS, &e.AdvisorStatus, &e.WaitlistPosition); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, e)
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(e enrollmentRow) int64 { return e.ID }))
}

// --- dönem ve şube yönetimi ---

type Term struct {
	ID               int64  `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	RegistrationOpen bool   `json:"registration_open"`
}

func (h *Handler) listTerms(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT id, code, name, registration_open FROM terms WHERE id > $1 ORDER BY id LIMIT $2`, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	var items []Term
	for rows.Next() {
		var t Term
		if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.RegistrationOpen); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, t)
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(t Term) int64 { return t.ID }))
}

func (h *Handler) createTerm(w http.ResponseWriter, r *http.Request) {
	var in Term
	if !httpx.Decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Code) == "" || strings.TrimSpace(in.Name) == "" {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "code ve name zorunlu")
		return
	}
	id, err := h.insertAudited(r, "term", in, func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO terms (code, name, registration_open) VALUES ($1, $2, $3) RETURNING id`,
			in.Code, in.Name, in.RegistrationOpen).Scan(&id)
	})
	in.ID = id
	if err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, in)
}

// setRegistration kayıt dönemini açar/kapatır ve değişikliği denetim kaydına yazar.
func (h *Handler) setRegistration(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		RegistrationOpen *bool `json:"registration_open"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.RegistrationOpen == nil {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "registration_open zorunlu")
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var old bool
	if err := tx.QueryRow(r.Context(),
		`SELECT registration_open FROM terms WHERE id = $1 FOR UPDATE`, id).Scan(&old); errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "dönem bulunamadı")
		return
	} else if err != nil {
		httpx.Internal(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE terms SET registration_open = $2 WHERE id = $1`, id, *in.RegistrationOpen); err != nil {
		httpx.Internal(w, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	if err := audit.Write(r.Context(), tx, audit.Entry{
		ActorID: &p.UserID, Action: "term.registration_changed", Entity: "term", EntityID: strconv.FormatInt(id, 10),
		Old: map[string]bool{"registration_open": old}, New: map[string]bool{"registration_open": *in.RegistrationOpen},
		IP: r.RemoteAddr,
	}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "registration_open": *in.RegistrationOpen})
}

type Section struct {
	ID            int64  `json:"id"`
	TermID        int64  `json:"term_id"`
	CourseID      int64  `json:"course_id"`
	Code          string `json:"code"`
	InstructorID  *int64 `json:"instructor_id"`
	Capacity      int    `json:"capacity"`
	EnrolledCount int    `json:"enrolled_count"`
	WaitlistSize  int    `json:"waitlist_size"`
	CourseCode    string `json:"course_code,omitempty"`
	CourseName    string `json:"course_name,omitempty"`
	ECTS          int    `json:"ects,omitempty"`
}

const sectionSelect = `
	SELECT s.id, s.term_id, s.course_id, s.code, s.instructor_id, s.capacity, s.enrolled_count,
	       (SELECT count(*) FROM enrollments e WHERE e.section_id = s.id AND e.status = 'waitlisted'),
	       c.code, c.name, c.ects
	  FROM sections s JOIN courses c ON c.id = s.course_id`

func scanSection(row pgx.Row) (Section, error) {
	var s Section
	err := row.Scan(&s.ID, &s.TermID, &s.CourseID, &s.Code, &s.InstructorID, &s.Capacity, &s.EnrolledCount, &s.WaitlistSize,
		&s.CourseCode, &s.CourseName, &s.ECTS)
	return s, err
}

func (h *Handler) listSections(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	var termID *int64
	if v := r.URL.Query().Get("term_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "term_id geçersiz")
			return
		}
		termID = &n
	}
	rows, err := h.pool.Query(r.Context(),
		sectionSelect+` WHERE s.id > $1 AND ($3::bigint IS NULL OR s.term_id = $3) ORDER BY s.id LIMIT $2`,
		after, limit+1, termID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	var items []Section
	for rows.Next() {
		s, err := scanSection(rows)
		if err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, s)
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(s Section) int64 { return s.ID }))
}

func (h *Handler) getSection(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	s, err := scanSection(h.pool.QueryRow(r.Context(), sectionSelect+` WHERE s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "şube bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (h *Handler) createSection(w http.ResponseWriter, r *http.Request) {
	var in Section
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.TermID < 1 || in.CourseID < 1 || strings.TrimSpace(in.Code) == "" || in.Capacity < 1 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "term_id, course_id, code ve capacity>0 zorunlu")
		return
	}
	id, err := h.insertAudited(r, "section", in, func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO sections (term_id, course_id, code, instructor_id, capacity)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			in.TermID, in.CourseID, in.Code, in.InstructorID, in.Capacity).Scan(&id)
	})
	in.ID = id
	if err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	in.EnrolledCount, in.WaitlistSize = 0, 0
	httpx.JSON(w, http.StatusCreated, in)
}

// insertAudited kaydı ve denetim satırını tek transaction'da yazar; ikisinden biri başarısız olursa hiçbiri kalmaz.
func (h *Handler) insertAudited(r *http.Request, entity string, newValue any, insert func(tx pgx.Tx) (int64, error)) (int64, error) {
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := insert(tx)
	if err != nil {
		return 0, err
	}
	p, _ := auth.PrincipalFrom(ctx)
	if err := audit.Write(ctx, tx, audit.Entry{
		ActorID: &p.UserID, Action: entity + ".create", Entity: entity, EntityID: strconv.FormatInt(id, 10),
		New: newValue, IP: r.RemoteAddr,
	}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
