package gradebook

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/access"
	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/grading"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func NewHandler(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// Routes çağıranın Authenticate middleware'i arkasında bağlanmalıdır.
func (h *Handler) Routes(r chi.Router) {
	instructor := auth.Require(auth.RoleInstructor)
	approvers := auth.Require(auth.RoleRegistrar, auth.RoleDeptSecretary)

	r.With(instructor).Put("/sections/{id}/grade-components", h.putComponents)
	r.With(instructor).Put("/sections/{id}/scores", h.putScores)
	r.With(instructor).Post("/sections/{id}/grades/publish", h.publish)
	r.With(auth.Require(auth.RoleInstructor, auth.RoleRegistrar, auth.RoleDeptSecretary)).
		Get("/sections/{id}/results", h.sectionResults)

	r.With(instructor).Post("/course-results/{id}/corrections", h.requestCorrection)
	r.With(approvers).Get("/grade-corrections", h.listCorrections)
	r.With(approvers).Post("/grade-corrections/{id}/decision", h.decideCorrection)

	r.Get("/students/{id}/results", h.studentResults)
	r.With(auth.Require(auth.RoleRegistrar)).Put("/programs/{id}/retake-policy", h.setRetakePolicy)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// lockSection şube satırını kilitler ve çağıranın bu şubenin öğretim üyesi olduğunu doğrular.
// Hata yanıtını kendisi yazar; false dönerse çağıran bir şey yazmamalı. Kilit tx bitene kadar sürer.
func lockSection(ctx context.Context, w http.ResponseWriter, tx pgx.Tx, sectionID, userID int64) (published bool, method string, ok bool) {
	var instructor *int64
	var publishedAt *string
	err := tx.QueryRow(ctx,
		`SELECT instructor_id, published_at::text, grading_method FROM sections WHERE id = $1 FOR UPDATE`, sectionID).
		Scan(&instructor, &publishedAt, &method)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "şube bulunamadı")
		return false, "", false
	}
	if err != nil {
		httpx.Internal(w, err)
		return false, "", false
	}
	if instructor == nil || *instructor != userID {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu şubenin öğretim üyesi değilsiniz")
		return false, "", false
	}
	return publishedAt != nil, method, true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool { return httpx.Decode(w, r, dst) }

// --- bileşen ağırlıkları ve yöntem ---

func (h *Handler) putComponents(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Method     string `json:"method"`
		Components []struct {
			Name   string `json:"name"`
			Weight int    `json:"weight"`
		} `json:"components"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Method != grading.MethodAbsolute && in.Method != grading.MethodRelative {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "method absolute veya relative olmalı")
		return
	}
	total, seen := 0, map[string]bool{}
	for _, c := range in.Components {
		if !slices.Contains(grading.Components, c.Name) || c.Weight < 1 || c.Weight > 100 || seen[c.Name] {
			httpx.Error(w, http.StatusBadRequest, "validation_error",
				"bileşen adı midterm|final|homework olmalı, tekrarsız ve ağırlığı 1-100 olmalı")
			return
		}
		seen[c.Name] = true
		total += c.Weight
	}
	if total != 100 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "ağırlıkların toplamı 100 olmalı")
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
	published, _, ok := lockSection(ctx, w, tx, sectionID, p.UserID)
	if !ok {
		return
	}
	if published {
		httpx.Error(w, http.StatusConflict, "grades_published", "notlar ilan edildi; değişiklik için düzeltme talebi açın")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM grade_components WHERE section_id = $1`, sectionID); err != nil {
		httpx.Internal(w, err)
		return
	}
	for _, c := range in.Components {
		if _, err := tx.Exec(ctx,
			`INSERT INTO grade_components (section_id, name, weight) VALUES ($1, $2, $3)`, sectionID, c.Name, c.Weight); err != nil {
			httpx.Internal(w, err)
			return
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE sections SET grading_method = $2 WHERE id = $1`, sectionID, in.Method); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "section.grade_components_set",
		Entity: "section", EntityID: itoa(sectionID), New: in, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, in)
}

// --- puan girişi ---

type scoreEntry struct {
	EnrollmentID int64   `json:"enrollment_id"`
	Component    string  `json:"component"`
	Kind         string  `json:"kind"`
	Score        float64 `json:"score"`
}

func (h *Handler) putScores(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Entries []scoreEntry `json:"entries"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Entries) == 0 || len(in.Entries) > 1000 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "entries 1-1000 eleman içermeli")
		return
	}
	ids := map[int64]bool{}
	for _, e := range in.Entries {
		if e.EnrollmentID < 1 || !slices.Contains(grading.Components, e.Component) ||
			!slices.Contains([]string{grading.KindRegular, grading.KindMakeup, grading.KindExcuse}, e.Kind) ||
			e.Score < 0 || e.Score > 100 {
			httpx.Error(w, http.StatusBadRequest, "validation_error",
				"her kayıt için enrollment_id, component (midterm|final|homework), kind (regular|makeup|excuse) ve 0-100 arası score gerekli")
			return
		}
		ids[e.EnrollmentID] = true
	}

	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	published, _, ok := lockSection(ctx, w, tx, sectionID, p.UserID)
	if !ok {
		return
	}
	if published {
		httpx.Error(w, http.StatusConflict, "grades_published", "notlar ilan edildi; giriş kilitlendi")
		return
	}

	idList := make([]int64, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	var valid int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM enrollments WHERE id = ANY($1) AND section_id = $2 AND status = 'enrolled'`,
		idList, sectionID).Scan(&valid); err != nil {
		httpx.Internal(w, err)
		return
	}
	if valid != len(idList) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_reference", "kayıtlardan biri bu şubeye ait değil veya aktif değil")
		return
	}
	// Ağırlığı tanımlı olmayan bileşene puan girilemez.
	rows, err := tx.Query(ctx, `SELECT name FROM grade_components WHERE section_id = $1`, sectionID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defined := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			httpx.Internal(w, err)
			return
		}
		defined[n] = true
	}
	rows.Close()
	for _, e := range in.Entries {
		if !defined[e.Component] {
			httpx.Error(w, http.StatusUnprocessableEntity, "component_not_defined", e.Component+" bileşeni bu şube için tanımlı değil")
			return
		}
	}

	for _, e := range in.Entries {
		if _, err := tx.Exec(ctx,
			`INSERT INTO component_scores (enrollment_id, component, kind, score, entered_by) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (enrollment_id, component, kind) DO UPDATE
			   SET score = EXCLUDED.score, entered_by = EXCLUDED.entered_by, entered_at = now()`,
			e.EnrollmentID, e.Component, e.Kind, e.Score, p.UserID); err != nil {
			httpx.Internal(w, err)
			return
		}
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "section.scores_entered",
		Entity: "section", EntityID: itoa(sectionID), New: map[string]int{"entries": len(in.Entries)}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"saved": len(in.Entries)})
}

// --- ilan ---

type publishedRow struct {
	ResultID     int64   `json:"result_id"`
	EnrollmentID int64   `json:"enrollment_id"`
	StudentID    int64   `json:"student_id"`
	Total        float64 `json:"total"`
	Letter       string  `json:"letter"`
}

// publish notları hesaplar, course_results'a yazar ve girişi kilitler. Tek transaction'dır:
// ya hepsi yazılır ya hiçbiri. Etkilenen tüm öğrencilerin GANO'su kuyruğa alınır.
func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
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
	published, method, ok := lockSection(ctx, w, tx, sectionID, p.UserID)
	if !ok {
		return
	}
	if published {
		httpx.Error(w, http.StatusConflict, "grades_published", "notlar zaten ilan edilmiş")
		return
	}

	weights := map[string]int{}
	rows, err := tx.Query(ctx, `SELECT name, weight FROM grade_components WHERE section_id = $1`, sectionID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	for rows.Next() {
		var n string
		var wt int
		if err := rows.Scan(&n, &wt); err != nil {
			rows.Close()
			httpx.Internal(w, err)
			return
		}
		weights[n] = wt
	}
	rows.Close()
	if len(weights) == 0 {
		httpx.Error(w, http.StatusConflict, "components_missing", "önce not bileşenlerini tanımlayın")
		return
	}

	type enr struct{ id, student int64 }
	var enrolled []enr
	rows, err = tx.Query(ctx, `SELECT id, student_id FROM enrollments WHERE section_id = $1 AND status = 'enrolled' ORDER BY id`, sectionID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	for rows.Next() {
		var e enr
		if err := rows.Scan(&e.id, &e.student); err != nil {
			rows.Close()
			httpx.Internal(w, err)
			return
		}
		enrolled = append(enrolled, e)
	}
	rows.Close()

	byEnr := map[int64]map[string]map[string]float64{}
	rows, err = tx.Query(ctx,
		`SELECT cs.enrollment_id, cs.component, cs.kind, cs.score::float8 FROM component_scores cs
		   JOIN enrollments e ON e.id = cs.enrollment_id WHERE e.section_id = $1`, sectionID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	for rows.Next() {
		var id int64
		var comp, kind string
		var score float64
		if err := rows.Scan(&id, &comp, &kind, &score); err != nil {
			rows.Close()
			httpx.Internal(w, err)
			return
		}
		if byEnr[id] == nil {
			byEnr[id] = map[string]map[string]float64{}
		}
		if byEnr[id][comp] == nil {
			byEnr[id][comp] = map[string]float64{}
		}
		byEnr[id][comp][kind] = score
	}
	rows.Close()

	totals := make([]float64, len(enrolled))
	for i, e := range enrolled {
		totals[i] = grading.Total(weights, grading.Effective(byEnr[e.id]))
	}
	letters := grading.Letters(method, totals)

	var termID, courseID int64
	if err := tx.QueryRow(ctx, `SELECT term_id, course_id FROM sections WHERE id = $1`, sectionID).Scan(&termID, &courseID); err != nil {
		httpx.Internal(w, err)
		return
	}
	out := make([]publishedRow, 0, len(enrolled))
	students := make([]int64, 0, len(enrolled))
	for i, e := range enrolled {
		point, _ := grading.PointOf(letters[i])
		var resultID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO course_results (student_id, course_id, term_id, letter, grade_point, passed, enrollment_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			e.student, courseID, termID, letters[i], point, grading.Passed(letters[i]), e.id).Scan(&resultID); err != nil {
			if !httpx.DBError(w, err) {
				httpx.Internal(w, err)
			}
			return
		}
		out = append(out, publishedRow{resultID, e.id, e.student, totals[i], letters[i]})
		students = append(students, e.student)
	}

	if _, err := tx.Exec(ctx, `UPDATE sections SET published_at = now() WHERE id = $1`, sectionID); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := Enqueue(ctx, tx, students); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "section.grades_published",
		Entity: "section", EntityID: itoa(sectionID),
		New: map[string]any{"method": method, "students": len(out)}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"method": method, "results": out})
}

// sectionResults şubenin ilan edilmiş sonuçlarını döner. Öğretim üyesi yalnızca kendi şubesini görür.
func (h *Handler) sectionResults(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	var instructor *int64
	err := h.pool.QueryRow(ctx, `SELECT instructor_id FROM sections WHERE id = $1`, sectionID).Scan(&instructor)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "şube bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if p.Role == auth.RoleInstructor && (instructor == nil || *instructor != p.UserID) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu şubenin öğretim üyesi değilsiniz")
		return
	}
	rows, err := h.pool.Query(ctx,
		`SELECT cr.id, cr.enrollment_id, cr.student_id, cr.letter FROM course_results cr
		   JOIN enrollments e ON e.id = cr.enrollment_id WHERE e.section_id = $1 ORDER BY cr.id`, sectionID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, eid, sid int64
		var letter string
		if err := rows.Scan(&id, &eid, &sid, &letter); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, map[string]any{"result_id": id, "enrollment_id": eid, "student_id": sid, "letter": letter})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- not düzeltmesi (çift onay) ---

func (h *Handler) requestCorrection(w http.ResponseWriter, r *http.Request) {
	resultID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		NewLetter string `json:"new_letter"`
		Reason    string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if _, valid := grading.PointOf(in.NewLetter); !valid || strings.TrimSpace(in.Reason) == "" {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "geçerli bir new_letter ve reason zorunlu")
		return
	}
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)

	var old string
	var instructor *int64
	err := h.pool.QueryRow(ctx,
		`SELECT cr.letter, s.instructor_id FROM course_results cr
		   JOIN enrollments e ON e.id = cr.enrollment_id JOIN sections s ON s.id = e.section_id
		  WHERE cr.id = $1`, resultID).Scan(&old, &instructor)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "sonuç bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if instructor == nil || *instructor != p.UserID {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu şubenin öğretim üyesi değilsiniz")
		return
	}
	if old == in.NewLetter {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "yeni not mevcut notla aynı")
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO grade_corrections (result_id, requested_by, old_letter, new_letter, reason)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`, resultID, p.UserID, old, in.NewLetter, in.Reason).Scan(&id); err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "grade_correction.requested",
		Entity: "course_result", EntityID: itoa(resultID),
		Old: map[string]string{"letter": old}, New: map[string]string{"letter": in.NewLetter, "reason": in.Reason},
		IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "status": "pending", "old_letter": old, "new_letter": in.NewLetter})
}

func (h *Handler) listCorrections(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains([]string{"pending", "approved", "rejected"}, status) {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "status pending|approved|rejected olmalı")
		return
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT id, result_id, requested_by, old_letter, new_letter, reason, status
		   FROM grade_corrections WHERE id > $1 AND ($3 = '' OR status = $3) ORDER BY id LIMIT $2`,
		after, limit+1, status)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID          int64  `json:"id"`
		ResultID    int64  `json:"result_id"`
		RequestedBy int64  `json:"requested_by"`
		OldLetter   string `json:"old_letter"`
		NewLetter   string `json:"new_letter"`
		Reason      string `json:"reason"`
		Status      string `json:"status"`
	}
	var items []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.ResultID, &x.RequestedBy, &x.OldLetter, &x.NewLetter, &x.Reason, &x.Status); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, x)
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x row) int64 { return x.ID }))
}

// decideCorrection ikinci kişi onayıdır: talebi yapan kişi kendi talebini onaylayamaz.
func (h *Handler) decideCorrection(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Approve *bool `json:"approve"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Approve == nil {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "approve (true|false) zorunlu")
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

	var resultID, requestedBy, studentID int64
	var oldL, newL, reason, status string
	err = tx.QueryRow(ctx,
		`SELECT gc.result_id, gc.requested_by, gc.old_letter, gc.new_letter, gc.reason, gc.status, cr.student_id
		   FROM grade_corrections gc JOIN course_results cr ON cr.id = gc.result_id
		  WHERE gc.id = $1 FOR UPDATE OF gc`, id).Scan(&resultID, &requestedBy, &oldL, &newL, &reason, &status, &studentID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "düzeltme talebi bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if status != "pending" {
		httpx.Error(w, http.StatusConflict, "already_decided", "talep zaten karara bağlanmış")
		return
	}
	if requestedBy == p.UserID {
		httpx.Error(w, http.StatusForbidden, "self_approval", "kendi talebinizi onaylayamazsınız (çift onay gerekir)")
		return
	}

	decision := "rejected"
	if *in.Approve {
		decision = "approved"
		// Sonuç satırı o sırada değişmiş olabilir; talep anındaki notla eşleşmiyorsa onaylanamaz.
		var current string
		if err := tx.QueryRow(ctx, `SELECT letter FROM course_results WHERE id = $1 FOR UPDATE`, resultID).Scan(&current); err != nil {
			httpx.Internal(w, err)
			return
		}
		if current != oldL {
			httpx.Error(w, http.StatusConflict, "stale_request", "sonuç talepten sonra değişmiş; yeni talep açın")
			return
		}
		point, _ := grading.PointOf(newL)
		if _, err := tx.Exec(ctx,
			`UPDATE course_results SET letter = $2, grade_point = $3, passed = $4 WHERE id = $1`,
			resultID, newL, point, grading.Passed(newL)); err != nil {
			httpx.Internal(w, err)
			return
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO grade_history (result_id, old_letter, new_letter, requested_by, approved_by, reason)
			 VALUES ($1, $2, $3, $4, $5, $6)`, resultID, oldL, newL, requestedBy, p.UserID, reason); err != nil {
			httpx.Internal(w, err)
			return
		}
		if err := Enqueue(ctx, tx, []int64{studentID}); err != nil {
			httpx.Internal(w, err)
			return
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE grade_corrections SET status = $2, decided_by = $3, decided_at = now() WHERE id = $1`,
		id, decision, p.UserID); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "grade_correction." + decision,
		Entity: "course_result", EntityID: itoa(resultID),
		Old: map[string]string{"letter": oldL}, New: map[string]string{"letter": newL}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "status": decision})
}

// --- öğrenci sonuçları ve politika ---

func (h *Handler) studentResults(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	st, ok := access.Authorize(w, r, h.pool, id)
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT cr.id, c.code, c.name, c.ects, t.code, cr.letter, cr.grade_point::float8, cr.passed, cr.exempt
		   FROM course_results cr JOIN courses c ON c.id = cr.course_id JOIN terms t ON t.id = cr.term_id
		  WHERE cr.student_id = $1 ORDER BY cr.term_id, c.code`, st.ID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var rid int64
		var code, name, term, letter string
		var ects int
		var point float64
		var passed, exempt bool
		if err := rows.Scan(&rid, &code, &name, &ects, &term, &letter, &point, &passed, &exempt); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, map[string]any{"result_id": rid, "course_code": code, "course_name": name, "ects": ects,
			"term": term, "letter": letter, "grade_point": point, "passed": passed, "exempt": exempt})
	}
	rows.Close()
	live, err := LoadGANO(ctx, h.pool, st.ID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	var cached float64
	if err := h.pool.QueryRow(ctx, `SELECT gano::float8 FROM students WHERE id = $1`, st.ID).Scan(&cached); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"student_id": st.ID, "gano": live, "gano_cached": cached, "results": items})
}

// setRetakePolicy tekrar-alma politikasını değiştirir ve programdaki TÜM öğrencilerin GANO'sunu
// kuyruğa alır (arka plan job'ı yeniden hesaplar).
func (h *Handler) setRetakePolicy(w http.ResponseWriter, r *http.Request) {
	programID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Policy string `json:"policy"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Policy != grading.PolicyLast && in.Policy != grading.PolicyBest {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "policy last veya best olmalı")
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
	var old string
	err = tx.QueryRow(ctx, `SELECT retake_policy FROM programs WHERE id = $1 FOR UPDATE`, programID).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "program bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE programs SET retake_policy = $2 WHERE id = $1`, programID, in.Policy); err != nil {
		httpx.Internal(w, err)
		return
	}
	var queued int64
	ct, err := tx.Exec(ctx,
		`INSERT INTO gano_jobs (student_id) SELECT id FROM students WHERE program_id = $1
		 ON CONFLICT (student_id) DO UPDATE SET enqueued_at = now()`, programID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	queued = ct.RowsAffected()
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "program.retake_policy_changed",
		Entity: "program", EntityID: itoa(programID),
		Old: map[string]string{"policy": old}, New: map[string]string{"policy": in.Policy}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"program_id": programID, "policy": in.Policy, "recalculating_students": queued})
}
