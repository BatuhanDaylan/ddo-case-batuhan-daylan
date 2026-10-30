package graduation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/testdb"
)

type env struct {
	pool    *pgxpool.Pool
	router  http.Handler
	iss     *auth.Issuer
	version int64
	term    int64
	sid     []int64 // students.id
	uid     []int64 // users.id
	reg     int64
	courses map[string]int64
}

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

func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	e := &env{pool: pool, courses: map[string]int64{}}
	e.reg = scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ('r@t','x','R','registrar') RETURNING id`)
	f := scan[int64](t, pool, `INSERT INTO faculties (code,name) VALUES ('F','F') RETURNING id`)
	d := scan[int64](t, pool, `INSERT INTO departments (faculty_id,code,name) VALUES ($1,'D','D') RETURNING id`, f)
	prog := scan[int64](t, pool, `INSERT INTO programs (department_id,code,name,total_ects_required) VALUES ($1,'P','P',240) RETURNING id`, d)
	e.term = scan[int64](t, pool, `INSERT INTO terms (code,name) VALUES ('T1','T1') RETURNING id`)
	for _, c := range []struct {
		code string
		ects int
	}{{"R1", 6}, {"R2", 6}, {"E1", 3}, {"E2", 3}, {"E3", 3}, {"OLD", 6}, {"NEW", 6}} {
		e.courses[c.code] = scan[int64](t, pool, `INSERT INTO courses (code,name,ects) VALUES ($1,$1,$2) RETURNING id`, c.code, c.ects)
	}
	e.version = scan[int64](t, pool, `INSERT INTO curriculum_versions (program_id,effective_year) VALUES ($1,2024) RETURNING id`, prog)
	for _, c := range []string{"R1", "R2"} {
		exec(t, pool, `INSERT INTO curriculum_courses (curriculum_version_id,course_id,semester,kind) VALUES ($1,$2,1,'required')`, e.version, e.courses[c])
	}
	for i := 0; i < 2; i++ {
		u := scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ($1,'x','S','student') RETURNING id`, fmt.Sprintf("s%d@t", i))
		s := scan[int64](t, pool, `INSERT INTO students (user_id,student_no,program_id,entry_year) VALUES ($1,$2,$3,2024) RETURNING id`, u, fmt.Sprintf("N%d", i), prog)
		e.uid, e.sid = append(e.uid, u), append(e.sid, s)
	}
	e.iss = auth.NewIssuer("integration-test-secret-integration-test", time.Hour)
	r := chi.NewRouter()
	r.Use(e.iss.Authenticate)
	NewHandler(pool).Routes(r)
	e.router = r
	return e
}

func (e *env) do(method, path string, user int64, role string, body any) (int, map[string]any) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	tok, _ := e.iss.Access(user, role)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *env) pass(t *testing.T, student int, code, letter string, point float64) {
	t.Helper()
	exec(t, e.pool, `INSERT INTO course_results (student_id,course_id,term_id,letter,grade_point,passed) VALUES ($1,$2,$3,$4,$5,true)`,
		e.sid[student], e.courses[code], e.term, letter, point)
}

func report(t *testing.T, e *env, student int) map[string]any {
	t.Helper()
	code, body := e.do("GET", fmt.Sprintf("/students/%d/graduation-audit", e.sid[student]), e.uid[student], auth.RoleStudent, nil)
	if code != 200 {
		t.Fatalf("denetim: %d %v", code, body)
	}
	return body["report"].(map[string]any)
}

func TestGraduationAuditEndToEnd(t *testing.T) {
	e := setup(t)
	rulesURL := fmt.Sprintf("/curriculum-versions/%d/graduation-rules", e.version)

	// Kural yokken 404.
	if c, _ := e.do("GET", fmt.Sprintf("/students/%d/graduation-audit", e.sid[0]), e.uid[0], auth.RoleStudent, nil); c != 404 {
		t.Errorf("kuralsız denetim: %d, beklenen 404", c)
	}

	rules := map[string]any{
		"min_gano": 2.0, "min_total_ects": 30,
		"pools": []any{
			map[string]any{"name": "A", "courses": []string{"E1", "E2"}, "min_courses": 1, "min_ects": 3},
			map[string]any{"name": "B", "courses": []string{"E2", "E3"}, "min_courses": 1, "min_ects": 3},
		},
	}
	// Yetki ve doğrulama: öğrenci kural yazamaz; geçersiz kural 400; olmayan ders 422.
	if c, _ := e.do("PUT", rulesURL, e.uid[0], auth.RoleStudent, rules); c != 403 {
		t.Errorf("öğrenci kural yazdı: %d", c)
	}
	if c, _ := e.do("PUT", rulesURL, e.reg, auth.RoleRegistrar, map[string]any{"min_gano": 9}); c != 400 {
		t.Errorf("geçersiz kural: %d, beklenen 400", c)
	}
	bad := map[string]any{"pools": []any{map[string]any{"name": "A", "courses": []string{"YOK"}, "min_courses": 1}}}
	if c, _ := e.do("PUT", rulesURL, e.reg, auth.RoleRegistrar, bad); c != 422 {
		t.Errorf("olmayan ders: %d, beklenen 422", c)
	}
	if c, b := e.do("PUT", rulesURL, e.reg, auth.RoleRegistrar, rules); c != 200 {
		t.Fatalf("kural kaydı: %d %v", c, b)
	}
	if n := scan[int](t, e.pool, `SELECT count(*) FROM audit_log WHERE action='graduation_rules.set'`); n != 1 {
		t.Errorf("kural değişikliği denetime yazılmadı")
	}

	// Hiçbir şey geçilmemiş: zorunlu dersler müfredattan gelir; her eksik tek raporda.
	rep := report(t, e, 0)
	if rep["can_graduate"] != false {
		t.Fatal("hiçbir ders geçilmemişken mezun görünüyor")
	}
	if got := toStrings(rep["required_missing"]); !slices.Equal(got, []string{"R1", "R2"}) {
		t.Errorf("eksik zorunlu = %v", got)
	}
	if len(rep["missing"].([]any)) < 3 { // zorunlu + 2 havuz (+ GANO'nun 0 olması + AKTS)
		t.Errorf("özet eksik: %v", rep["missing"])
	}

	// E2 ortak ders: yalnızca E2 geçildiyse A ya da B'den birini doyurur, ikisini değil.
	e.pass(t, 0, "R1", "AA", 4)
	e.pass(t, 0, "R2", "AA", 4)
	e.pass(t, 0, "E2", "AA", 4)
	rep = report(t, e, 0)
	if rep["can_graduate"] != false || rep["required_ok"] != true {
		t.Fatalf("E2 tek başına iki havuzu doyuramaz: %+v", rep)
	}
	// E3 geçilince en uygun atama: E2→A, E3→B (ya da tersi); iki havuz da tamam. Toplam 21 AKTS < 30 → AKTS eksik.
	e.pass(t, 0, "E3", "BA", 3.5)
	rep = report(t, e, 0)
	for _, p := range rep["pools"].([]any) {
		if p.(map[string]any)["ok"] != true {
			t.Errorf("iki havuz da doyurulabilirdi: %v", rep["pools"])
		}
	}
	if rep["ects_ok"] != false || rep["can_graduate"] != false {
		t.Errorf("AKTS eksikken mezun: %v", rep)
	}
	// Eşiği sağlayacak kadar ders ekle: 30 AKTS için E1 (3) + OLD (6) → 6+6+3+3+3+6 = 27 → hâlâ eksik; NEW ile 33.
	e.pass(t, 0, "E1", "CC", 2)
	e.pass(t, 0, "OLD", "CC", 2)
	e.pass(t, 0, "NEW", "CC", 2)
	rep = report(t, e, 0)
	if rep["can_graduate"] != true {
		t.Fatalf("tüm şartlar sağlandı, mezun olabilmeli: %v", rep)
	}
}

func TestEquivalenceCountsForRequiredAndTotalNotDoubled(t *testing.T) {
	e := setup(t)
	rules := map[string]any{"min_gano": 0.0, "min_total_ects": 0, "required": []string{"OLD"}, "pools": []any{}}
	if c, b := e.do("PUT", fmt.Sprintf("/curriculum-versions/%d/graduation-rules", e.version), e.reg, auth.RoleRegistrar, rules); c != 200 {
		t.Fatalf("kural: %d %v", c, b)
	}
	// Öğrenci OLD yerine NEW'i geçti ve "OLD yerine NEW sayılır" denkliği var.
	e.pass(t, 1, "NEW", "BB", 3)
	rep := func() map[string]any {
		c, body := e.do("GET", fmt.Sprintf("/students/%d/graduation-audit", e.sid[1]), e.uid[1], auth.RoleStudent, nil)
		if c != 200 {
			t.Fatalf("denetim: %d", c)
		}
		return body["report"].(map[string]any)
	}
	if rep()["required_ok"] != false {
		t.Fatal("denklik yokken zorunlu ders sağlanmış görünüyor")
	}
	exec(t, e.pool, `INSERT INTO course_equivalences (course_id, substitute_course_id) VALUES ($1,$2)`, e.courses["OLD"], e.courses["NEW"])
	r := rep()
	if r["required_ok"] != true || r["can_graduate"] != true {
		t.Errorf("denklikle zorunlu ders sağlanmalıydı: %v", r)
	}
	if r["total_ects"].(float64) != 6 { // yalnızca gerçekten geçilen NEW (6); denklik çift saymaz
		t.Errorf("toplam AKTS %v, beklenen 6", r["total_ects"])
	}
}

func TestGraduationAuditAccessControl(t *testing.T) {
	e := setup(t)
	exec(t, e.pool, `INSERT INTO graduation_rules (curriculum_version_id, rules) VALUES ($1, '{"min_gano":0,"min_total_ects":0,"pools":[]}')`, e.version)
	url := fmt.Sprintf("/students/%d/graduation-audit", e.sid[0])
	for _, c := range []struct {
		name string
		user int64
		role string
		want int
	}{
		{"kendisi", e.uid[0], auth.RoleStudent, 200},
		{"başka öğrenci", e.uid[1], auth.RoleStudent, 403},
		{"öğrenci işleri", e.reg, auth.RoleRegistrar, 200},
		{"öğretim üyesi", e.reg, auth.RoleInstructor, 403},
		{"raporlama", e.reg, auth.RoleReporting, 403},
	} {
		if code, _ := e.do("GET", url, c.user, c.role, nil); code != c.want {
			t.Errorf("%s: %d, beklenen %d", c.name, code, c.want)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
