package reports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/testdb"
)

func scan[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// F-8: bölüm bazlı başarı, doluluk ve GANO dağılımı. Elle hesaplanabilen küçük bir veriyle doğrulanır.
func TestReportsComputeExpectedNumbers(t *testing.T) {
	pool := testdb.New(t)
	iss := auth.NewIssuer("integration-test-secret-integration-test", time.Hour)
	r := chi.NewRouter()
	r.Use(iss.Authenticate)
	NewHandler(pool).Routes(r)

	f := scan[int64](t, pool, `INSERT INTO faculties (code,name) VALUES ('F','F') RETURNING id`)
	d1 := scan[int64](t, pool, `INSERT INTO departments (faculty_id,code,name) VALUES ($1,'D1','Bölüm 1') RETURNING id`, f)
	d2 := scan[int64](t, pool, `INSERT INTO departments (faculty_id,code,name) VALUES ($1,'D2','Bölüm 2') RETURNING id`, f)
	p1 := scan[int64](t, pool, `INSERT INTO programs (department_id,code,name,total_ects_required) VALUES ($1,'P1','P1',240) RETURNING id`, d1)
	term := scan[int64](t, pool, `INSERT INTO terms (code,name) VALUES ('T','T') RETURNING id`)
	c1 := scan[int64](t, pool, `INSERT INTO courses (code,name,ects,department_id) VALUES ('A','A',5,$1) RETURNING id`, d1)
	c2 := scan[int64](t, pool, `INSERT INTO courses (code,name,ects,department_id) VALUES ('B','B',5,$1) RETURNING id`, d2)
	exec(t, pool, `INSERT INTO sections (term_id,course_id,code,capacity,enrolled_count) VALUES ($1,$2,'01',40,30), ($1,$3,'01',10,10)`, term, c1, c2)
	sec1 := scan[int64](t, pool, `SELECT id FROM sections WHERE course_id=$1`, c1)

	reportingUser := scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ('rep@t','x','R','reporting') RETURNING id`)
	studentUser := scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ('s@t','x','S','student') RETURNING id`)

	// 4 öğrenci, GANO 0.5 / 2.2 / 3.6 / 4.0: her biri farklı aralıkta (4.0 son aralığa girmeli).
	// Bölüm 1 dersinde ilki kalır (FF), diğer üçü geçer (BB); ilk öğrenci Bölüm 2 dersinden AA alır.
	var first int64
	for i, g := range []float64{0.5, 2.2, 3.6, 4.0} {
		u := scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ($1,'x','S','student') RETURNING id`, fmt.Sprintf("x%d@t", i))
		s := scan[int64](t, pool, `INSERT INTO students (user_id,student_no,program_id,entry_year,gano) VALUES ($1,$2,$3,2024,$4) RETURNING id`, u, fmt.Sprintf("N%d", i), p1, g)
		letter, point, passed := "BB", 3.0, true
		if i == 0 {
			first, letter, point, passed = s, "FF", 0.0, false
		}
		exec(t, pool, `INSERT INTO course_results (student_id,course_id,term_id,letter,grade_point,passed) VALUES ($1,$2,$3,$4,$5,$6)`, s, c1, term, letter, point, passed)
	}
	exec(t, pool, `INSERT INTO course_results (student_id,course_id,term_id,letter,grade_point,passed) VALUES ($1,$2,$3,'AA',4.0,true)`, first, c2, term)
	exec(t, pool, `INSERT INTO enrollments (section_id,student_id,status) VALUES ($1,$2,'waitlisted')`, sec1, first) // 1 bekleyen

	do := func(user int64, role, path string) (int, map[string]any) {
		req := httptest.NewRequest("GET", path, bytes.NewReader(nil))
		tok, _ := iss.Access(user, role)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// Yetki ve parametre doğrulaması.
	if c, _ := do(studentUser, auth.RoleStudent, fmt.Sprintf("/reports/occupancy?term_id=%d", term)); c != 403 {
		t.Errorf("öğrenci rapor gördü: %d", c)
	}
	if c, _ := do(reportingUser, auth.RoleInstructor, fmt.Sprintf("/reports/occupancy?term_id=%d", term)); c != 403 {
		t.Errorf("öğretim üyesi rapor gördü: %d", c)
	}
	if c, _ := do(reportingUser, auth.RoleReporting, "/reports/occupancy"); c != 400 {
		t.Errorf("term_id yok: %d, beklenen 400", c)
	}

	// Başarı: Bölüm 1 → 4 sonuç, 3 geçen (%75), ort. (0+3+3+3)/4 = 2.25; Bölüm 2 → 1 sonuç, %100, 4.00.
	_, b := do(reportingUser, auth.RoleReporting, fmt.Sprintf("/reports/department-success?term_id=%d", term))
	items := b["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("başarı raporu: %v", b)
	}
	i0, i1 := items[0].(map[string]any), items[1].(map[string]any)
	if i0["results"].(float64) != 4 || i0["passed"].(float64) != 3 || i0["pass_rate"].(float64) != 75 || i0["avg_grade_point"].(float64) != 2.25 {
		t.Errorf("Bölüm 1 başarısı: %v", i0)
	}
	if i1["results"].(float64) != 1 || i1["pass_rate"].(float64) != 100 || i1["avg_grade_point"].(float64) != 4 {
		t.Errorf("Bölüm 2 başarısı: %v", i1)
	}

	// Doluluk: Bölüm 1 → kontenjan 40, kayıtlı 30 (%75), 1 bekleyen; Bölüm 2 → 10/10 (%100).
	_, b = do(reportingUser, auth.RoleRegistrar, fmt.Sprintf("/reports/occupancy?term_id=%d", term))
	occ := b["items"].([]any)
	o0, o1 := occ[0].(map[string]any), occ[1].(map[string]any)
	if o0["capacity"].(float64) != 40 || o0["enrolled"].(float64) != 30 || o0["fill_rate"].(float64) != 75 || o0["waitlisted"].(float64) != 1 {
		t.Errorf("Bölüm 1 doluluğu: %v", o0)
	}
	if o1["fill_rate"].(float64) != 100 {
		t.Errorf("Bölüm 2 doluluğu: %v", o1)
	}

	// GANO dağılımı: 0.5 → [0,1); 2.2 → [2,2.5); 3.6 ve 4.0 → [3.5,4.0] (4.0 son aralığa dahil).
	_, b = do(reportingUser, auth.RoleReporting, "/reports/gano-distribution")
	want := map[string]float64{"0.00-0.99": 1, "1.00-1.99": 0, "2.00-2.49": 1, "2.50-2.99": 0, "3.00-3.49": 0, "3.50-4.00": 2}
	for _, bk := range b["buckets"].([]any) {
		m := bk.(map[string]any)
		if m["count"].(float64) != want[m["range"].(string)] {
			t.Errorf("aralık %v: %v, beklenen %v", m["range"], m["count"], want[m["range"].(string)])
		}
	}
	if b["total_students"].(float64) != 4 {
		t.Errorf("toplam öğrenci: %v", b["total_students"])
	}
	// Süzgeç: Bölüm 2'nin öğrencisi yok.
	_, b = do(reportingUser, auth.RoleReporting, fmt.Sprintf("/reports/gano-distribution?department_id=%d", d2))
	if b["total_students"].(float64) != 0 {
		t.Errorf("Bölüm 2 öğrencisi yok ama %v", b["total_students"])
	}
	if c, _ := do(reportingUser, auth.RoleReporting, "/reports/gano-distribution?program_id=abc"); c != 400 {
		t.Errorf("geçersiz süzgeç: %d, beklenen 400", c)
	}
}
