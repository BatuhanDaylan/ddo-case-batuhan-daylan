package gradebook

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/grading"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

// ObjectionRoutes not itirazı uç noktalarını bağlar (Authenticate arkasında).
func (h *Handler) ObjectionRoutes(r chi.Router) {
	r.With(auth.Require(auth.RoleStudent)).Post("/course-results/{id}/objections", h.fileObjection)
	r.With(auth.Require(auth.RoleStudent)).Get("/me/objections", h.myObjections)
	r.With(auth.Require(auth.RoleInstructor)).Get("/sections/{id}/objections", h.sectionObjections)
	r.With(auth.Require(auth.RoleInstructor)).Post("/grade-objections/{id}/decision", h.decideObjection)
}

// fileObjection: POST /course-results/{id}/objections {"reason": "..."}
// Öğrenci yalnızca KENDİ ilan edilmiş sonucuna itiraz edebilir; sonuç başına aynı anda tek açık itiraz olur.
func (h *Handler) fileObjection(w http.ResponseWriter, r *http.Request) {
	resultID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2000 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "reason zorunlu (en fazla 2000 karakter)")
		return
	}
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	var studentID, owner int64
	err := h.pool.QueryRow(ctx,
		`SELECT cr.student_id, st.user_id FROM course_results cr JOIN students st ON st.id = cr.student_id WHERE cr.id = $1`,
		resultID).Scan(&studentID, &owner)
	// Başkasının sonucu için "yok" deriz.
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != p.UserID) {
		httpx.Error(w, http.StatusNotFound, "not_found", "sonuç bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
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
		`INSERT INTO grade_objections (result_id, student_id, reason) VALUES ($1, $2, $3) RETURNING id`,
		resultID, studentID, in.Reason).Scan(&id); err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "grade_objection.filed", Entity: "course_result",
		EntityID: itoa(resultID), New: map[string]string{"reason": in.Reason}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "status": "open", "result_id": resultID})
}

type objectionRow struct {
	ID           int64   `json:"id"`
	ResultID     int64   `json:"result_id"`
	StudentNo    string  `json:"student_no,omitempty"`
	CourseCode   string  `json:"course_code"`
	Letter       string  `json:"letter"`
	Reason       string  `json:"reason"`
	Status       string  `json:"status"`
	Response     *string `json:"response"`
	CorrectionID *int64  `json:"correction_id"`
}

const objectionSelect = `SELECT o.id, o.result_id, st.student_no, c.code, cr.letter, o.reason, o.status, o.response, o.correction_id
	FROM grade_objections o JOIN course_results cr ON cr.id = o.result_id
	JOIN students st ON st.id = o.student_id JOIN courses c ON c.id = cr.course_id`

func scanObjections(rows pgx.Rows) ([]objectionRow, error) {
	defer rows.Close()
	var out []objectionRow
	for rows.Next() {
		var x objectionRow
		if err := rows.Scan(&x.ID, &x.ResultID, &x.StudentNo, &x.CourseCode, &x.Letter, &x.Reason, &x.Status, &x.Response, &x.CorrectionID); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (h *Handler) myObjections(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), objectionSelect+` WHERE st.user_id = $1 AND o.id > $2 ORDER BY o.id LIMIT $3`, p.UserID, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	items, err := scanObjections(rows)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x objectionRow) int64 { return x.ID }))
}

// sectionObjections: GET /sections/{id}/objections — öğretim üyesi yalnızca KENDİ şubesinin itirazlarını görür.
func (h *Handler) sectionObjections(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
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
	if instructor == nil || *instructor != p.UserID {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu şubenin öğretim üyesi değilsiniz")
		return
	}
	rows, err := h.pool.Query(ctx, objectionSelect+`
		JOIN enrollments e ON e.id = cr.enrollment_id WHERE e.section_id = $1 AND o.id > $2 ORDER BY o.id LIMIT $3`, sectionID, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	items, err := scanObjections(rows)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x objectionRow) int64 { return x.ID }))
}

// decideObjection: POST /grade-objections/{id}/decision
//
//	{"accept": false, "response": "gerekçe"}                       → itiraz reddedilir
//	{"accept": true,  "new_letter": "BB", "response": "gerekçe"}   → itiraz kabul edilir ve not düzeltme talebi açılır
//
// Kabul, notu TEK BAŞINA değiştirmez: öğretim üyesi adına bir düzeltme talebi açılır ve ikinci kişi
// (bölüm sekreteri / öğrenci işleri) onaylayana kadar not aynı kalır (T-7 çift onay).
func (h *Handler) decideObjection(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Accept    *bool  `json:"accept"`
		NewLetter string `json:"new_letter"`
		Response  string `json:"response"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.Accept == nil || strings.TrimSpace(in.Response) == "" {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "accept (true|false) ve response zorunlu")
		return
	}
	if *in.Accept {
		if _, valid := grading.PointOf(in.NewLetter); !valid {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "kabul için geçerli bir new_letter zorunlu")
			return
		}
	}

	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var resultID int64
	var status, current string
	var instructor *int64
	err = tx.QueryRow(ctx,
		`SELECT o.result_id, o.status, cr.letter, s.instructor_id
		   FROM grade_objections o JOIN course_results cr ON cr.id = o.result_id
		   JOIN enrollments e ON e.id = cr.enrollment_id JOIN sections s ON s.id = e.section_id
		  WHERE o.id = $1 FOR UPDATE OF o`, id).Scan(&resultID, &status, &current, &instructor)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "itiraz bulunamadı")
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
	if status != "open" {
		httpx.Error(w, http.StatusConflict, "already_decided", "itiraz zaten karara bağlanmış")
		return
	}

	decision := "rejected"
	var correctionID *int64
	if *in.Accept {
		decision = "accepted"
		if in.NewLetter == current {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "yeni not mevcut notla aynı")
			return
		}
		var cid int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO grade_corrections (result_id, requested_by, old_letter, new_letter, reason)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			resultID, p.UserID, current, in.NewLetter, "Öğrenci itirazı kabul edildi: "+in.Response).Scan(&cid); err != nil {
			if !httpx.DBError(w, err) {
				httpx.Internal(w, err)
			}
			return
		}
		correctionID = &cid
	}
	if _, err := tx.Exec(ctx,
		`UPDATE grade_objections SET status = $2, response = $3, correction_id = $4, decided_by = $5, decided_at = now() WHERE id = $1`,
		id, decision, in.Response, correctionID, p.UserID); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "grade_objection." + decision, Entity: "course_result",
		EntityID: itoa(resultID), New: map[string]any{"response": in.Response, "new_letter": in.NewLetter}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	out := map[string]any{"id": id, "status": decision}
	if correctionID != nil {
		out["correction_id"] = *correctionID
		out["note"] = "not, ikinci kişi onaylayana kadar değişmez"
	}
	httpx.JSON(w, http.StatusOK, out)
}
