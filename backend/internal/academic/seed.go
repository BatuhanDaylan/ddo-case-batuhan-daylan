package academic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/rules"
)

type seedCourse struct {
	code, name string
	ects       int
}

var seedCourses = []seedCourse{
	{"BM101", "Programlamaya Giriş", 6},
	{"MAT101", "Matematik I", 6},
	{"FIZ101", "Fizik I", 5},
	{"BM102", "Nesne Yönelimli Programlama", 6},
	{"MAT102", "Matematik II", 6},
	{"BM104", "Ayrık Matematik", 5},
	{"BM105", "Veri Yapıları", 6},
	{"BM301", "Yapay Zekâya Giriş", 5},
}

// code → (semester, kind)
type placement struct {
	code     string
	semester int
	kind     string
}

var curriculum2021 = []placement{
	{"BM101", 1, "required"}, {"MAT101", 1, "required"}, {"FIZ101", 1, "required"},
	{"BM102", 2, "required"}, {"MAT102", 2, "required"}, {"BM104", 2, "required"},
	{"BM301", 5, "elective"},
}

// 2024 müfredatı: Ayrık Matematik yerine Veri Yapıları geldi. 2021 girişliler etkilenmez.
var curriculum2024 = []placement{
	{"BM101", 1, "required"}, {"MAT101", 1, "required"}, {"FIZ101", 1, "required"},
	{"BM102", 2, "required"}, {"MAT102", 2, "required"}, {"BM105", 2, "required"},
	{"BM301", 5, "elective"},
}

// SeedDemo örnek akademik yapıyı ve demo öğrenciyi kurar. İdempotenttir:
// fakülte zaten varsa hiçbir şey yapmaz. Kullanıcılar (auth.SeedDemoUsers) önceden var olmalı.
func SeedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM faculties)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var facultyID, deptID, programID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO faculties (code, name) VALUES ('MUH', 'Mühendislik Fakültesi') RETURNING id`).Scan(&facultyID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO departments (faculty_id, code, name) VALUES ($1, 'BM', 'Bilgisayar Mühendisliği') RETURNING id`,
		facultyID).Scan(&deptID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO programs (department_id, code, name, total_ects_required)
		 VALUES ($1, 'BM-LS', 'Bilgisayar Mühendisliği Lisans', 240) RETURNING id`, deptID).Scan(&programID); err != nil {
		return err
	}

	courseIDs := map[string]int64{}
	for _, c := range seedCourses {
		var id int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO courses (code, name, ects, department_id) VALUES ($1, $2, $3, $4) RETURNING id`,
			c.code, c.name, c.ects, deptID).Scan(&id); err != nil {
			return err
		}
		courseIDs[c.code] = id
	}

	// Ön koşullar: BM102 → BM101 ve (MAT101 veya FIZ101); BM105 ve BM301 → BM102.
	prereqs := map[string]rules.Node{
		"BM102": {Op: rules.OpAnd, Args: []rules.Node{
			{CourseID: courseIDs["BM101"]},
			{Op: rules.OpOr, Args: []rules.Node{{CourseID: courseIDs["MAT101"]}, {CourseID: courseIDs["FIZ101"]}}},
		}},
		"BM105": {CourseID: courseIDs["BM102"]},
		"BM301": {CourseID: courseIDs["BM102"]},
	}
	for code, expr := range prereqs {
		raw, err := json.Marshal(expr)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO course_prerequisites (course_id, expr) VALUES ($1, $2)`, courseIDs[code], raw); err != nil {
			return err
		}
	}

	for year, plan := range map[int][]placement{2021: curriculum2021, 2024: curriculum2024} {
		if err := insertVersion(ctx, tx, programID, year, plan, courseIDs); err != nil {
			return err
		}
	}

	var studentUser, advisorUser int64
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = 'ogrenci@uninet.test'`).Scan(&studentUser); err != nil {
		return fmt.Errorf("demo öğrenci kullanıcısı yok: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = 'danisman@uninet.test'`).Scan(&advisorUser); err != nil {
		return fmt.Errorf("demo danışman kullanıcısı yok: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO students (user_id, student_no, program_id, entry_year, advisor_id)
		 VALUES ($1, '20210001', $2, 2021, $3)`, studentUser, programID, advisorUser); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertVersion(ctx context.Context, tx pgx.Tx, programID int64, year int, plan []placement, ids map[string]int64) error {
	var vid int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO curriculum_versions (program_id, effective_year) VALUES ($1, $2) RETURNING id`,
		programID, year).Scan(&vid); err != nil {
		return err
	}
	for _, p := range plan {
		if _, err := tx.Exec(ctx,
			`INSERT INTO curriculum_courses (curriculum_version_id, course_id, semester, kind) VALUES ($1, $2, $3, $4)`,
			vid, ids[p.code], p.semester, p.kind); err != nil {
			return err
		}
	}
	return nil
}
