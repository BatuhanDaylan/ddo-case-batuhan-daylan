package gradebook

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

// RosterRoutes şube listesi ve not yapılandırması okuma uç noktalarını bağlar (Authenticate arkasında).
func (h *Handler) RosterRoutes(r chi.Router) {
	staff := auth.Require(auth.RoleInstructor, auth.RoleRegistrar, auth.RoleDeptSecretary)
	r.With(staff).Get("/sections/{id}/roster", h.roster)
	r.With(staff).Get("/sections/{id}/grading", h.grading)
}

// canSeeSection öğretim üyesinin yalnızca KENDİ şubesini görmesini sağlar; öğrenci işleri ve bölüm
// sekreteri tüm şubeleri görür. Yanıtı kendisi yazar; false dönerse çağıran bir şey yazmamalı.
func (h *Handler) canSeeSection(w http.ResponseWriter, r *http.Request, sectionID int64) (published bool, method string, ok bool) {
	p, _ := auth.PrincipalFrom(r.Context())
	var instructor *int64
	var publishedAt *string
	err := h.pool.QueryRow(r.Context(),
		`SELECT instructor_id, published_at::text, grading_method FROM sections WHERE id = $1`, sectionID).
		Scan(&instructor, &publishedAt, &method)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "şube bulunamadı")
		return false, "", false
	}
	if err != nil {
		httpx.Internal(w, err)
		return false, "", false
	}
	if p.Role == auth.RoleInstructor && (instructor == nil || *instructor != p.UserID) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu şubenin öğretim üyesi değilsiniz")
		return false, "", false
	}
	return publishedAt != nil, method, true
}

type rosterScore struct {
	Component string  `json:"component"`
	Kind      string  `json:"kind"`
	Score     float64 `json:"score"`
}

type rosterRow struct {
	EnrollmentID int64         `json:"enrollment_id"`
	StudentID    int64         `json:"student_id"`
	StudentNo    string        `json:"student_no"`
	FullName     string        `json:"full_name"`
	Scores       []rosterScore `json:"scores"`
}

// roster: GET /sections/{id}/roster — şubenin kayıtlı öğrencileri ve girilmiş puanları (keyset sayfalı).
func (h *Handler) roster(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	if _, _, ok := h.canSeeSection(w, r, sectionID); !ok {
		return
	}
	ctx := r.Context()
	rows, err := h.pool.Query(ctx,
		`SELECT e.id, st.id, st.student_no, u.full_name
		   FROM enrollments e JOIN students st ON st.id = e.student_id JOIN users u ON u.id = st.user_id
		  WHERE e.section_id = $1 AND e.status = 'enrolled' AND e.id > $2 ORDER BY e.id LIMIT $3`, sectionID, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	var items []rosterRow
	index := map[int64]int{}
	var ids []int64
	for rows.Next() {
		var x rosterRow
		if err := rows.Scan(&x.EnrollmentID, &x.StudentID, &x.StudentNo, &x.FullName); err != nil {
			rows.Close()
			httpx.Internal(w, err)
			return
		}
		x.Scores = []rosterScore{}
		index[x.EnrollmentID] = len(items)
		items = append(items, x)
		ids = append(ids, x.EnrollmentID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpx.Internal(w, err)
		return
	}
	if len(ids) > 0 {
		srows, err := h.pool.Query(ctx,
			`SELECT enrollment_id, component, kind, score::float8 FROM component_scores WHERE enrollment_id = ANY($1) ORDER BY enrollment_id, component, kind`, ids)
		if err != nil {
			httpx.Internal(w, err)
			return
		}
		defer srows.Close()
		for srows.Next() {
			var eid int64
			var s rosterScore
			if err := srows.Scan(&eid, &s.Component, &s.Kind, &s.Score); err != nil {
				httpx.Internal(w, err)
				return
			}
			items[index[eid]].Scores = append(items[index[eid]].Scores, s)
		}
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x rosterRow) int64 { return x.EnrollmentID }))
}

// grading: GET /sections/{id}/grading — not yöntemi, bileşen ağırlıkları ve ilan durumu.
func (h *Handler) grading(w http.ResponseWriter, r *http.Request) {
	sectionID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	published, method, ok := h.canSeeSection(w, r, sectionID)
	if !ok {
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT name, weight FROM grade_components WHERE section_id = $1 ORDER BY name`, sectionID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	comps := []map[string]any{}
	for rows.Next() {
		var name string
		var weight int
		if err := rows.Scan(&name, &weight); err != nil {
			httpx.Internal(w, err)
			return
		}
		comps = append(comps, map[string]any{"name": name, "weight": weight})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"section_id": sectionID, "method": method, "published": published, "components": comps})
}
