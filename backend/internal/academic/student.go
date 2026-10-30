package academic

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/rules"
)

type studentInfo struct {
	ID, UserID, ProgramID int64
	EntryYear             int
	AdvisorID             *int64
}

// authorizeStudent {id} öğrencisini yükler ve çağıranın görme yetkisini denetler.
// Öğrenci yalnızca kendisini, danışman yalnızca danışanlarını; öğrenci işleri ve bölüm sekreteri
// tümünü görür; diğer roller 403 alır. Yanıtı kendisi yazar, false dönerse çağıran bir şey yazmamalı.
func (h *Handler) authorizeStudent(w http.ResponseWriter, r *http.Request) (studentInfo, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return studentInfo{}, false
	}
	st := studentInfo{ID: id}
	err := h.pool.QueryRow(r.Context(),
		`SELECT user_id, program_id, entry_year, advisor_id FROM students WHERE id = $1`, id).
		Scan(&st.UserID, &st.ProgramID, &st.EntryYear, &st.AdvisorID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "öğrenci bulunamadı")
		return st, false
	}
	if err != nil {
		internal(w, err)
		return st, false
	}

	p, _ := auth.PrincipalFrom(r.Context())
	allowed := false
	switch p.Role {
	case auth.RoleRegistrar, auth.RoleDeptSecretary:
		allowed = true
	case auth.RoleStudent:
		allowed = p.UserID == st.UserID
	case auth.RoleAdvisor:
		allowed = st.AdvisorID != nil && *st.AdvisorID == p.UserID
	}
	if !allowed {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu öğrenci için yetkiniz yok")
		return st, false
	}
	return st, true
}

type requiredCourse struct {
	CourseID  int64  `json:"course_id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	ECTS      int    `json:"ects"`
	Semester  int    `json:"semester"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Due       string `json:"due"`
	ViaCourse string `json:"via_course,omitempty"`
}

// requiredCourses: GET /students/{id}/required-courses?as_of=2024
//
// Zaman yolculuğu: öğrencinin tabi olduğu müfredat, GİRİŞ YILINA göre çözülür (as_of'a göre değil);
// as_of yalnızca öğrencinin o tarihte kaçıncı öğretim yılında olduğunu belirler. Böylece
// "2021 girişli öğrencinin 2024 itibarıyla alması gereken dersler" sorusu, sonradan açılan
// 2024 müfredatından etkilenmeden doğru döner. Her ders için durum (geçti/muaf/denk/bekliyor) ve
// vade (overdue/current/future) verilir; gecikmiş veya güncel dönem zorunlu dersleri `must_take`
// listesindedir.
func (h *Handler) requiredCourses(w http.ResponseWriter, r *http.Request) {
	st, ok := h.authorizeStudent(w, r)
	if !ok {
		return
	}
	asOf := 0
	if v := r.URL.Query().Get("as_of"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1990 || n > 2100 {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "as_of 1990-2100 arası bir yıl olmalı")
			return
		}
		asOf = n
	} else {
		asOf = st.EntryYear // verilmezse giriş yılı (1. yıl)
	}

	ctx := r.Context()
	vid, err := ResolveVersion(ctx, h.pool, st.ProgramID, st.EntryYear)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "no_curriculum", "bu giriş yılı için tanımlı müfredat yok")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	v, err := h.loadVersion(ctx, vid)
	if err != nil {
		internal(w, err)
		return
	}

	passed, exempt, eq, codes, err := h.studentProgress(ctx, st.ID)
	if err != nil {
		internal(w, err)
		return
	}

	studyYear := rules.StudyYear(st.EntryYear, asOf)
	all := make([]requiredCourse, 0, len(v.Courses))
	mustTake := []requiredCourse{}
	for _, c := range v.Courses {
		status, via := rules.CourseStatus(c.CourseID, passed, exempt, eq)
		rc := requiredCourse{
			CourseID: c.CourseID, Code: c.Code, Name: c.Name, ECTS: c.ECTS, Semester: c.Semester,
			Kind: c.Kind, Status: status, Due: rules.Due(c.Semester, studyYear), ViaCourse: codes[via],
		}
		all = append(all, rc)
		if rc.Kind == "required" && rc.Status == rules.StatusPending && rc.Due != "future" {
			mustTake = append(mustTake, rc)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"student_id":     st.ID,
		"entry_year":     st.EntryYear,
		"as_of":          asOf,
		"study_year":     studyYear,
		"curriculum":     map[string]any{"version_id": v.ID, "effective_year": v.EffectiveYear},
		"courses":        all,
		"must_take":      mustTake,
		"must_take_ects": sumECTS(mustTake),
	})
}

func sumECTS(cs []requiredCourse) int {
	n := 0
	for _, c := range cs {
		n += c.ECTS
	}
	return n
}

// studentProgress öğrencinin geçtiği ve muaf olduğu dersleri ile ilgili denkliklerini döner.
func (h *Handler) studentProgress(ctx context.Context, studentID int64) (passed, exempt map[int64]bool,
	eq rules.Equivalences, codes map[int64]string, err error) {
	passed, exempt, eq, codes = map[int64]bool{}, map[int64]bool{}, rules.Equivalences{}, map[int64]string{}

	rows, err := h.pool.Query(ctx, `SELECT course_id, exempt FROM course_results WHERE student_id = $1 AND passed`, studentID)
	if err != nil {
		return
	}
	for rows.Next() {
		var id int64
		var ex bool
		if err = rows.Scan(&id, &ex); err != nil {
			rows.Close()
			return
		}
		passed[id] = true
		if ex {
			exempt[id] = true
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return
	}

	erows, err := h.pool.Query(ctx, `SELECT course_id, substitute_course_id FROM course_equivalences`)
	if err != nil {
		return
	}
	for erows.Next() {
		var c, s int64
		if err = erows.Scan(&c, &s); err != nil {
			erows.Close()
			return
		}
		eq[c] = append(eq[c], s)
	}
	erows.Close()
	if err = erows.Err(); err != nil {
		return
	}

	crows, err := h.pool.Query(ctx, `SELECT id, code FROM courses`)
	if err != nil {
		return
	}
	defer crows.Close()
	for crows.Next() {
		var id int64
		var code string
		if err = crows.Scan(&id, &code); err != nil {
			return
		}
		codes[id] = code
	}
	err = crows.Err()
	return
}

// --- denklik yönetimi ve muafiyet ---

type equivalence struct {
	ID                 int64 `json:"id"`
	CourseID           int64 `json:"course_id"`
	SubstituteCourseID int64 `json:"substitute_course_id"`
}

func (h *Handler) listEquivalences(w http.ResponseWriter, r *http.Request) {
	listPage(w, r, h.pool,
		`SELECT id, course_id, substitute_course_id FROM course_equivalences
		  WHERE id > $1 AND $3::bigint IS NULL ORDER BY id LIMIT $2`,
		nil, func(rows pgx.Rows) (equivalence, error) {
			var e equivalence
			return e, rows.Scan(&e.ID, &e.CourseID, &e.SubstituteCourseID)
		}, func(e equivalence) int64 { return e.ID })
}

func (h *Handler) createEquivalence(w http.ResponseWriter, r *http.Request) {
	var in equivalence
	if !decode(w, r, &in) {
		return
	}
	if in.CourseID < 1 || in.SubstituteCourseID < 1 || in.CourseID == in.SubstituteCourseID {
		httpx.Error(w, http.StatusBadRequest, "validation_error",
			"course_id ve substitute_course_id zorunlu ve farklı olmalı")
		return
	}
	id, err := h.createAudited(r.Context(), r, "course_equivalence", func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO course_equivalences (course_id, substitute_course_id) VALUES ($1, $2) RETURNING id`,
			in.CourseID, in.SubstituteCourseID).Scan(&id)
	}, in)
	if err != nil {
		if !dbError(w, err) {
			internal(w, err)
		}
		return
	}
	in.ID = id
	httpx.JSON(w, http.StatusCreated, in)
}

func (h *Handler) deleteEquivalence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var old equivalence
	err = tx.QueryRow(ctx,
		`DELETE FROM course_equivalences WHERE id = $1 RETURNING id, course_id, substitute_course_id`, id).
		Scan(&old.ID, &old.CourseID, &old.SubstituteCourseID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "denklik kaydı bulunamadı")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	p, _ := auth.PrincipalFrom(ctx)
	if err := audit.Write(ctx, tx, audit.Entry{
		ActorID: &p.UserID, Action: "course_equivalence.delete", Entity: "course_equivalence",
		EntityID: strconv.FormatInt(id, 10), Old: old, IP: r.RemoteAddr,
	}); err != nil {
		internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createExemption: POST /students/{id}/exemptions  {"course_id":…, "term_id":…}
// Yalnızca öğrenci işleri. Ders başarılmış sayılır, notu yoktur, GANO'ya girmez.
func (h *Handler) createExemption(w http.ResponseWriter, r *http.Request) {
	st, ok := h.authorizeStudent(w, r)
	if !ok {
		return
	}
	var in struct {
		CourseID int64 `json:"course_id"`
		TermID   int64 `json:"term_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.CourseID < 1 || in.TermID < 1 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "course_id ve term_id zorunlu")
		return
	}
	id, err := h.createAudited(r.Context(), r, "exemption", func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO course_results (student_id, course_id, term_id, letter, grade_point, passed, exempt)
			 VALUES ($1, $2, $3, 'MU', 0, true, true) RETURNING id`,
			st.ID, in.CourseID, in.TermID).Scan(&id)
	}, map[string]any{"student_id": st.ID, "course_id": in.CourseID, "term_id": in.TermID})
	if err != nil {
		if !dbError(w, err) {
			internal(w, err)
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"id": id, "student_id": st.ID, "course_id": in.CourseID, "term_id": in.TermID, "exempt": true,
	})
}
