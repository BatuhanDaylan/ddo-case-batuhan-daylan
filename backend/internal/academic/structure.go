package academic

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/httpx"
)

// decode gövdeyi sınırlar ve bilinmeyen alanları reddeder.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_body", "istek gövdesi geçersiz")
		return false
	}
	return true
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// --- fakülte ---

type Faculty struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func (h *Handler) listFaculties(w http.ResponseWriter, r *http.Request) {
	listPage(w, r, h.pool,
		// listPage her zaman 3 parametre geçer; fakültelerde süzgeç yok, $3 her zaman NULL.
		`SELECT id, code, name FROM faculties WHERE id > $1 AND $3::bigint IS NULL ORDER BY id LIMIT $2`,
		nil, func(rows pgx.Rows) (Faculty, error) {
			var f Faculty
			return f, rows.Scan(&f.ID, &f.Code, &f.Name)
		}, func(f Faculty) int64 { return f.ID })
}

func (h *Handler) createFaculty(w http.ResponseWriter, r *http.Request) {
	var in Faculty
	if !decode(w, r, &in) {
		return
	}
	if blank(in.Code) || blank(in.Name) {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "code ve name zorunlu")
		return
	}
	id, err := h.createAudited(r.Context(), r, "faculty", func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO faculties (code, name) VALUES ($1, $2) RETURNING id`, in.Code, in.Name).Scan(&id)
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

// --- bölüm ---

type Department struct {
	ID        int64  `json:"id"`
	FacultyID int64  `json:"faculty_id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
}

func (h *Handler) listDepartments(w http.ResponseWriter, r *http.Request) {
	facultyID, ok := optInt64(w, r, "faculty_id")
	if !ok {
		return
	}
	listPage(w, r, h.pool,
		`SELECT id, faculty_id, code, name FROM departments
		  WHERE id > $1 AND ($3::bigint IS NULL OR faculty_id = $3) ORDER BY id LIMIT $2`,
		facultyID, func(rows pgx.Rows) (Department, error) {
			var d Department
			return d, rows.Scan(&d.ID, &d.FacultyID, &d.Code, &d.Name)
		}, func(d Department) int64 { return d.ID })
}

func (h *Handler) createDepartment(w http.ResponseWriter, r *http.Request) {
	var in Department
	if !decode(w, r, &in) {
		return
	}
	if blank(in.Code) || blank(in.Name) || in.FacultyID < 1 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "faculty_id, code ve name zorunlu")
		return
	}
	id, err := h.createAudited(r.Context(), r, "department", func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO departments (faculty_id, code, name) VALUES ($1, $2, $3) RETURNING id`,
			in.FacultyID, in.Code, in.Name).Scan(&id)
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

// --- program ---

type Program struct {
	ID                int64  `json:"id"`
	DepartmentID      int64  `json:"department_id"`
	Code              string `json:"code"`
	Name              string `json:"name"`
	TotalECTSRequired int    `json:"total_ects_required"`
}

func (h *Handler) listPrograms(w http.ResponseWriter, r *http.Request) {
	deptID, ok := optInt64(w, r, "department_id")
	if !ok {
		return
	}
	listPage(w, r, h.pool,
		`SELECT id, department_id, code, name, total_ects_required FROM programs
		  WHERE id > $1 AND ($3::bigint IS NULL OR department_id = $3) ORDER BY id LIMIT $2`,
		deptID, func(rows pgx.Rows) (Program, error) {
			var p Program
			return p, rows.Scan(&p.ID, &p.DepartmentID, &p.Code, &p.Name, &p.TotalECTSRequired)
		}, func(p Program) int64 { return p.ID })
}

func (h *Handler) createProgram(w http.ResponseWriter, r *http.Request) {
	var in Program
	if !decode(w, r, &in) {
		return
	}
	if blank(in.Code) || blank(in.Name) || in.DepartmentID < 1 || in.TotalECTSRequired < 1 {
		httpx.Error(w, http.StatusBadRequest, "validation_error",
			"department_id, code, name ve total_ects_required zorunlu")
		return
	}
	id, err := h.createAudited(r.Context(), r, "program", func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO programs (department_id, code, name, total_ects_required)
			 VALUES ($1, $2, $3, $4) RETURNING id`,
			in.DepartmentID, in.Code, in.Name, in.TotalECTSRequired).Scan(&id)
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

// --- ders ---

type Course struct {
	ID           int64  `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	ECTS         int    `json:"ects"`
	DepartmentID *int64 `json:"department_id"`
}

func (h *Handler) listCourses(w http.ResponseWriter, r *http.Request) {
	deptID, ok := optInt64(w, r, "department_id")
	if !ok {
		return
	}
	listPage(w, r, h.pool,
		`SELECT id, code, name, ects, department_id FROM courses
		  WHERE id > $1 AND ($3::bigint IS NULL OR department_id = $3) ORDER BY id LIMIT $2`,
		deptID, func(rows pgx.Rows) (Course, error) {
			var c Course
			return c, rows.Scan(&c.ID, &c.Code, &c.Name, &c.ECTS, &c.DepartmentID)
		}, func(c Course) int64 { return c.ID })
}

func (h *Handler) createCourse(w http.ResponseWriter, r *http.Request) {
	var in Course
	if !decode(w, r, &in) {
		return
	}
	if blank(in.Code) || blank(in.Name) || in.ECTS < 1 || in.ECTS > 60 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "code, name ve 1-60 arası ects zorunlu")
		return
	}
	id, err := h.createAudited(r.Context(), r, "course", func(tx pgx.Tx) (int64, error) {
		var id int64
		return id, tx.QueryRow(r.Context(),
			`INSERT INTO courses (code, name, ects, department_id) VALUES ($1, $2, $3, $4) RETURNING id`,
			in.Code, in.Name, in.ECTS, in.DepartmentID).Scan(&id)
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
