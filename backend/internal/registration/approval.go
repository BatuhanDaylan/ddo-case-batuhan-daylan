package registration

import (
	"errors"
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

// ApprovalRoutes danışman onayı ve ekle-sil süresi uç noktalarını bağlar (Authenticate arkasında).
func (h *Handler) ApprovalRoutes(r chi.Router) {
	advisor := auth.Require(auth.RoleAdvisor)
	r.With(advisor).Get("/advisor/enrollments", h.advisorList)
	r.With(advisor).Get("/advisor/at-risk", h.atRisk)
	r.With(advisor).Post("/enrollments/{id}/advisor-decision", h.advisorDecision)
	r.With(auth.Require(auth.RoleRegistrar, auth.RoleDeptSecretary)).Put("/terms/{id}/drop-deadline", h.setDropDeadline)
}

type advisorRow struct {
	EnrollmentID  int64  `json:"enrollment_id"`
	StudentNo     string `json:"student_no"`
	StudentID     int64  `json:"student_id"`
	CourseCode    string `json:"course_code"`
	SectionID     int64  `json:"section_id"`
	Status        string `json:"status"`
	AdvisorStatus string `json:"advisor_status"`
}

// advisorList: GET /advisor/enrollments?status=pending|approved|rejected
// Danışman yalnızca kendi danışanlarının kayıtlarını görür.
func (h *Handler) advisorList(w http.ResponseWriter, r *http.Request) {
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
	p, _ := auth.PrincipalFrom(r.Context())
	rows, err := h.pool.Query(r.Context(),
		`SELECT e.id, st.student_no, st.id, c.code, s.id, e.status, e.advisor_status
		   FROM enrollments e JOIN students st ON st.id = e.student_id
		   JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id
		  WHERE st.advisor_id = $1 AND e.status <> 'dropped' AND e.id > $2 AND ($3 = '' OR e.advisor_status = $3)
		  ORDER BY e.id LIMIT $4`, p.UserID, after, status, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	var items []advisorRow
	for rows.Next() {
		var x advisorRow
		if err := rows.Scan(&x.EnrollmentID, &x.StudentNo, &x.StudentID, &x.CourseCode, &x.SectionID, &x.Status, &x.AdvisorStatus); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, x)
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x advisorRow) int64 { return x.EnrollmentID }))
}

// advisorDecision: POST /enrollments/{id}/advisor-decision {"approve": true|false}
// Onay kaydı kesinleştirir. Ret kaydı düşürür; boşalan koltuk AYNI transaction'da bekleme listesinin
// ilk kişisine atanır ve ekle-sil süresi dolmuş olsa bile geçerlidir (idari işlem).
func (h *Handler) advisorDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Approve *bool `json:"approve"`
	}
	if !httpx.Decode(w, r, &in) {
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

	var studentID int64
	var advisorID *int64
	var status, advisorStatus string
	err = tx.QueryRow(ctx,
		`SELECT e.student_id, st.advisor_id, e.status, e.advisor_status
		   FROM enrollments e JOIN students st ON st.id = e.student_id WHERE e.id = $1`, id).
		Scan(&studentID, &advisorID, &status, &advisorStatus)
	// Başkasının danışanı için "yok" deriz: kayıt kimlikleri sızmasın.
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (advisorID == nil || *advisorID != p.UserID)) {
		httpx.Error(w, http.StatusNotFound, "not_found", ErrEnrollmentNotFound.Error())
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if status == StatusDropped {
		httpx.Error(w, http.StatusConflict, "enrollment_dropped", "kayıt zaten bırakılmış")
		return
	}
	if advisorStatus != "pending" {
		httpx.Error(w, http.StatusConflict, "already_decided", "bu kayıt için danışman kararı zaten verilmiş")
		return
	}

	decision := "rejected"
	var promoted *int64
	if *in.Approve {
		decision = "approved"
	} else {
		res, err := dropTx(ctx, tx, studentID, id, false)
		if err != nil {
			httpx.Internal(w, err)
			return
		}
		promoted = res.PromotedEnrollmentID
	}
	if _, err := tx.Exec(ctx,
		`UPDATE enrollments SET advisor_status = $2, advisor_decided_by = $3, advisor_decided_at = now() WHERE id = $1`,
		id, decision, p.UserID); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "enrollment.advisor_" + decision,
		Entity: "enrollment", EntityID: itoa(id),
		Old: map[string]string{"advisor_status": advisorStatus}, New: map[string]string{"advisor_status": decision}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	out := map[string]any{"enrollment_id": id, "advisor_status": decision}
	if promoted != nil {
		out["promoted_enrollment_id"] = *promoted
	}
	httpx.JSON(w, http.StatusOK, out)
}

// setDropDeadline: PUT /terms/{id}/drop-deadline {"deadline": "2026-10-20T23:59:00Z" | null}
func (h *Handler) setDropDeadline(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Deadline *time.Time `json:"deadline"`
	}
	if !httpx.Decode(w, r, &in) {
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
	var old *time.Time
	err = tx.QueryRow(ctx, `SELECT drop_deadline FROM terms WHERE id = $1 FOR UPDATE`, id).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "dönem bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE terms SET drop_deadline = $2 WHERE id = $1`, id, in.Deadline); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "term.drop_deadline_changed", Entity: "term",
		EntityID: itoa(id), Old: map[string]*time.Time{"deadline": old}, New: map[string]*time.Time{"deadline": in.Deadline}, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"term_id": id, "drop_deadline": in.Deadline})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
