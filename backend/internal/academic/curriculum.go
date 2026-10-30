package academic

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

type CurriculumCourse struct {
	CourseID int64  `json:"course_id"`
	Code     string `json:"code,omitempty"`
	Name     string `json:"name,omitempty"`
	ECTS     int    `json:"ects,omitempty"`
	Semester int    `json:"semester"`
	Kind     string `json:"kind"`
}

type CurriculumVersion struct {
	ID            int64              `json:"id"`
	ProgramID     int64              `json:"program_id"`
	EffectiveYear int                `json:"effective_year"`
	CreatedAt     time.Time          `json:"created_at"`
	Courses       []CurriculumCourse `json:"courses,omitempty"`
}

// ResolveVersion girişi entryYear olan öğrencinin tabi olduğu müfredat sürümünü bulur:
// programın, entryYear'dan önce (veya o yıl) yürürlüğe girmiş en yeni sürümü.
// Sonradan açılan yeni sürümler bu sonucu değiştirmez; eski öğrenciler etkilenmez.
func ResolveVersion(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, programID int64, entryYear int) (int64, error) {
	var id int64
	err := q.QueryRow(ctx,
		`SELECT id FROM curriculum_versions
		  WHERE program_id = $1 AND effective_year <= $2
		  ORDER BY effective_year DESC LIMIT 1`, programID, entryYear).Scan(&id)
	return id, err
}

func (h *Handler) loadVersion(ctx context.Context, id int64) (CurriculumVersion, error) {
	var v CurriculumVersion
	err := h.pool.QueryRow(ctx,
		`SELECT id, program_id, effective_year, created_at FROM curriculum_versions WHERE id = $1`, id).
		Scan(&v.ID, &v.ProgramID, &v.EffectiveYear, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	rows, err := h.pool.Query(ctx,
		`SELECT cc.course_id, c.code, c.name, c.ects, cc.semester, cc.kind
		   FROM curriculum_courses cc JOIN courses c ON c.id = cc.course_id
		  WHERE cc.curriculum_version_id = $1 ORDER BY cc.semester, c.code`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var c CurriculumCourse
		if err := rows.Scan(&c.CourseID, &c.Code, &c.Name, &c.ECTS, &c.Semester, &c.Kind); err != nil {
			return v, err
		}
		v.Courses = append(v.Courses, c)
	}
	return v, rows.Err()
}

func (h *Handler) listVersions(w http.ResponseWriter, r *http.Request) {
	programID, ok := pathID(w, r)
	if !ok {
		return
	}
	listPage(w, r, h.pool,
		`SELECT id, program_id, effective_year, created_at FROM curriculum_versions
		  WHERE id > $1 AND program_id = $3 ORDER BY id LIMIT $2`,
		&programID, func(rows pgx.Rows) (CurriculumVersion, error) {
			var v CurriculumVersion
			return v, rows.Scan(&v.ID, &v.ProgramID, &v.EffectiveYear, &v.CreatedAt)
		}, func(v CurriculumVersion) int64 { return v.ID })
}

func (h *Handler) getVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := h.loadVersion(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "müfredat sürümü bulunamadı")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (h *Handler) createVersion(w http.ResponseWriter, r *http.Request) {
	programID, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		EffectiveYear int                `json:"effective_year"`
		Courses       []CurriculumCourse `json:"courses"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.EffectiveYear < 1990 || in.EffectiveYear > 2100 || len(in.Courses) == 0 {
		httpx.Error(w, http.StatusBadRequest, "validation_error",
			"effective_year (1990-2100) ve en az bir ders zorunlu")
		return
	}
	seen := map[int64]bool{}
	for _, c := range in.Courses {
		if c.CourseID < 1 || c.Semester < 1 || c.Semester > 12 || (c.Kind != "required" && c.Kind != "elective") {
			httpx.Error(w, http.StatusBadRequest, "validation_error",
				"her ders için course_id, semester (1-12) ve kind (required|elective) gerekli")
			return
		}
		if seen[c.CourseID] {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "aynı ders müfredatta iki kez yer alamaz")
			return
		}
		seen[c.CourseID] = true
	}

	id, err := h.createAudited(r.Context(), r, "curriculum_version", func(tx pgx.Tx) (int64, error) {
		var id int64
		if err := tx.QueryRow(r.Context(),
			`INSERT INTO curriculum_versions (program_id, effective_year) VALUES ($1, $2) RETURNING id`,
			programID, in.EffectiveYear).Scan(&id); err != nil {
			return 0, err
		}
		for _, c := range in.Courses {
			if _, err := tx.Exec(r.Context(),
				`INSERT INTO curriculum_courses (curriculum_version_id, course_id, semester, kind)
				 VALUES ($1, $2, $3, $4)`, id, c.CourseID, c.Semester, c.Kind); err != nil {
				return 0, err
			}
		}
		return id, nil
	}, map[string]any{"program_id": programID, "effective_year": in.EffectiveYear, "course_count": len(in.Courses)})
	if err != nil {
		if !dbError(w, err) {
			internal(w, err)
		}
		return
	}
	v, err := h.loadVersion(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// studentCurriculum öğrencinin tabi olduğu müfredatı döner.
// Erişim: öğrenci yalnızca kendisini, danışman yalnızca danışanlarını görür;
// öğrenci işleri ve bölüm sekreteri tümünü; diğer roller 403 alır.
func (h *Handler) studentCurriculum(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var (
		userID, programID int64
		advisorID         *int64
		entryYear         int
	)
	err := h.pool.QueryRow(r.Context(),
		`SELECT user_id, program_id, entry_year, advisor_id FROM students WHERE id = $1`, id).
		Scan(&userID, &programID, &entryYear, &advisorID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "öğrenci bulunamadı")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}

	p, _ := auth.PrincipalFrom(r.Context())
	allowed := false
	switch p.Role {
	case auth.RoleRegistrar, auth.RoleDeptSecretary:
		allowed = true
	case auth.RoleStudent:
		allowed = p.UserID == userID
	case auth.RoleAdvisor:
		allowed = advisorID != nil && *advisorID == p.UserID
	}
	if !allowed {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu öğrencinin müfredatını görme yetkiniz yok")
		return
	}

	vid, err := ResolveVersion(r.Context(), h.pool, programID, entryYear)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "no_curriculum", "bu giriş yılı için tanımlı müfredat yok")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	v, err := h.loadVersion(r.Context(), vid)
	if err != nil {
		internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
