package gradebook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/grading"
	"github.com/batuhan-daylan/uninet/internal/testdb"
)

type env struct {
	pool     *pgxpool.Pool
	router   http.Handler
	iss      *auth.Issuer
	section  int64
	course   int64
	term     int64
	program  int64
	students []int64 // students.id
	enrolls  []int64 // enrollments.id (students ile aynı sırada)
	users    map[string]int64
}

func (e *env) token(role string) string {
	tok, _ := e.iss.Access(e.users[role], role)
	return tok
}

func (e *env) do(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func mustScan[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// setup: bir şube, o şubeye "enrolled" n öğrenci ve her rolden bir kullanıcı kurar.
func setup(t *testing.T, n int) *env {
	t.Helper()
	pool := testdb.New(t)
	e := &env{pool: pool, users: map[string]int64{}}
	for _, role := range []string{auth.RoleInstructor, auth.RoleRegistrar, auth.RoleDeptSecretary, auth.RoleStudent, "other_instructor"} {
		r := role
		if role == "other_instructor" {
			r = auth.RoleInstructor
		}
		e.users[role] = mustScan[int64](t, pool,
			`INSERT INTO users (email, password_hash, full_name, role) VALUES ($1, 'x', $1, $2) RETURNING id`, role+"@t.test", r)
	}
	f := mustScan[int64](t, pool, `INSERT INTO faculties (code, name) VALUES ('F', 'F') RETURNING id`)
	d := mustScan[int64](t, pool, `INSERT INTO departments (faculty_id, code, name) VALUES ($1, 'D', 'D') RETURNING id`, f)
	e.program = mustScan[int64](t, pool, `INSERT INTO programs (department_id, code, name, total_ects_required) VALUES ($1, 'P', 'P', 240) RETURNING id`, d)
	e.course = mustScan[int64](t, pool, `INSERT INTO courses (code, name, ects) VALUES ('C1', 'C1', 6) RETURNING id`)
	e.term = mustScan[int64](t, pool, `INSERT INTO terms (code, name, registration_open) VALUES ('T1', 'T1', true) RETURNING id`)
	e.section = mustScan[int64](t, pool,
		`INSERT INTO sections (term_id, course_id, code, instructor_id, capacity, enrolled_count) VALUES ($1, $2, '01', $3, 100, $4) RETURNING id`,
		e.term, e.course, e.users[auth.RoleInstructor], n)
	for i := 0; i < n; i++ {
		uid := mustScan[int64](t, pool,
			`INSERT INTO users (email, password_hash, full_name, role) VALUES ($1, 'x', 's', 'student') RETURNING id`, fmt.Sprintf("s%d@t.test", i))
		sid := mustScan[int64](t, pool,
			`INSERT INTO students (user_id, student_no, program_id, entry_year) VALUES ($1, $2, $3, 2024) RETURNING id`, uid, fmt.Sprintf("N%d", i), e.program)
		eid := mustScan[int64](t, pool,
			`INSERT INTO enrollments (section_id, student_id, status) VALUES ($1, $2, 'enrolled') RETURNING id`, e.section, sid)
		e.students, e.enrolls = append(e.students, sid), append(e.enrolls, eid)
	}

	e.iss = auth.NewIssuer("integration-test-secret-integration-test", time.Hour)
	r := chi.NewRouter()
	r.Use(e.iss.Authenticate)
	NewHandler(pool).Routes(r)
	e.router = r
	return e
}

func path(format string, a ...any) string { return fmt.Sprintf(format, a...) }

func (e *env) setComponents(t *testing.T, method string) {
	t.Helper()
	code, body := e.do(t, "PUT", path("/sections/%d/grade-components", e.section), e.token(auth.RoleInstructor), map[string]any{
		"method": method, "components": []map[string]any{{"name": "midterm", "weight": 40}, {"name": "final", "weight": 60}},
	})
	if code != 200 {
		t.Fatalf("bileşenler: %d %v", code, body)
	}
}

func (e *env) score(t *testing.T, i int, comp, kind string, score float64) map[string]any {
	return map[string]any{"enrollment_id": e.enrolls[i], "component": comp, "kind": kind, "score": score}
}

func TestPublishFlowAndLocking(t *testing.T) {
	e := setup(t, 3)
	e.setComponents(t, "absolute")
	instr := e.token(auth.RoleInstructor)

	// Öğrenci 0: 90/90 → AA. Öğrenci 1: final 20 ama bütünleme 80 → (60*.4... ) hesap aşağıda. Öğrenci 2: 30/30 → FF.
	code, body := e.do(t, "PUT", path("/sections/%d/scores", e.section), instr, map[string]any{"entries": []any{
		e.score(t, 0, "midterm", "regular", 90), e.score(t, 0, "final", "regular", 90),
		e.score(t, 1, "midterm", "regular", 80), e.score(t, 1, "final", "regular", 20), e.score(t, 1, "final", "makeup", 80),
		e.score(t, 2, "midterm", "regular", 30), e.score(t, 2, "final", "regular", 30),
	}})
	if code != 200 {
		t.Fatalf("puan girişi: %d %v", code, body)
	}

	// Başka şubenin öğretim üyesi, öğrenci ve yetkisiz rol girişi yapamaz.
	if c, _ := e.do(t, "PUT", path("/sections/%d/scores", e.section), e.token("other_instructor"), map[string]any{"entries": []any{e.score(t, 0, "midterm", "regular", 1)}}); c != 403 {
		t.Errorf("başka öğretim üyesi: %d, beklenen 403", c)
	}
	if c, _ := e.do(t, "PUT", path("/sections/%d/scores", e.section), e.token(auth.RoleStudent), map[string]any{"entries": []any{e.score(t, 0, "midterm", "regular", 1)}}); c != 403 {
		t.Errorf("öğrenci: %d, beklenen 403", c)
	}
	// Tanımsız bileşen reddedilir.
	if c, _ := e.do(t, "PUT", path("/sections/%d/scores", e.section), instr, map[string]any{"entries": []any{e.score(t, 0, "homework", "regular", 1)}}); c != 422 {
		t.Errorf("tanımsız bileşen: %d, beklenen 422", c)
	}

	code, body = e.do(t, "POST", path("/sections/%d/grades/publish", e.section), instr, nil)
	if code != 200 {
		t.Fatalf("ilan: %d %v", code, body)
	}
	got := map[int64]string{}
	for _, it := range body["results"].([]any) {
		m := it.(map[string]any)
		got[int64(m["enrollment_id"].(float64))] = m["letter"].(string)
	}
	// Öğrenci 1: vize 80*0.4=32 + bütünleme 80*0.6=48 = 80 → BB (bütünleme normal finalin yerine geçti).
	want := map[int64]string{e.enrolls[0]: "AA", e.enrolls[1]: "BB", e.enrolls[2]: "FF"}
	for id, l := range want {
		if got[id] != l {
			t.Errorf("kayıt %d: %s, beklenen %s", id, got[id], l)
		}
	}

	// İlandan sonra giriş kilitlenir; ikinci ilan ve bileşen değişikliği reddedilir.
	if c, _ := e.do(t, "PUT", path("/sections/%d/scores", e.section), instr, map[string]any{"entries": []any{e.score(t, 0, "midterm", "regular", 1)}}); c != 409 {
		t.Errorf("ilandan sonra giriş: %d, beklenen 409", c)
	}
	if c, _ := e.do(t, "POST", path("/sections/%d/grades/publish", e.section), instr, nil); c != 409 {
		t.Errorf("ikinci ilan: %d, beklenen 409", c)
	}
	e.setComponentsExpect(t, 409)

	// Etkilenen tüm öğrenciler kuyruğa girmiş olmalı; işçi çalışınca GANO'ları yazılır.
	if n := mustScan[int](t, e.pool, `SELECT count(*) FROM gano_jobs`); n != 3 {
		t.Errorf("kuyruktaki öğrenci %d, beklenen 3", n)
	}
	if n, err := ProcessBatch(context.Background(), e.pool, 100); err != nil || n != 3 {
		t.Fatalf("işçi: n=%d err=%v", n, err)
	}
	g := mustScan[float64](t, e.pool, `SELECT gano::float8 FROM students WHERE id=$1`, e.students[0])
	if g != 4 {
		t.Errorf("AA alan öğrencinin GANO'su %v, beklenen 4", g)
	}
}

func (e *env) setComponentsExpect(t *testing.T, want int) {
	t.Helper()
	code, _ := e.do(t, "PUT", path("/sections/%d/grade-components", e.section), e.token(auth.RoleInstructor), map[string]any{
		"method": "absolute", "components": []map[string]any{{"name": "final", "weight": 100}},
	})
	if code != want {
		t.Errorf("bileşen değişikliği: %d, beklenen %d", code, want)
	}
}

func TestRelativeMethodMatchesPureFunction(t *testing.T) {
	e := setup(t, 5)
	e.setComponents(t, "relative")
	totals := []float64{95, 80, 70, 60, 45}
	var entries []any
	for i, tot := range totals {
		entries = append(entries, e.score(t, i, "midterm", "regular", tot), e.score(t, i, "final", "regular", tot))
	}
	instr := e.token(auth.RoleInstructor)
	if c, b := e.do(t, "PUT", path("/sections/%d/scores", e.section), instr, map[string]any{"entries": entries}); c != 200 {
		t.Fatalf("giriş: %d %v", c, b)
	}
	code, body := e.do(t, "POST", path("/sections/%d/grades/publish", e.section), instr, nil)
	if code != 200 {
		t.Fatalf("ilan: %d %v", code, body)
	}
	want := grading.RelativeLetters(totals)
	for i, it := range body["results"].([]any) {
		if l := it.(map[string]any)["letter"].(string); l != want[i] {
			t.Errorf("öğrenci %d: %s, saf fonksiyon %s", i, l, want[i])
		}
	}
}

func TestCorrectionNeedsSecondPersonAndKeepsHistory(t *testing.T) {
	e := setup(t, 1)
	e.setComponents(t, "absolute")
	instr := e.token(auth.RoleInstructor)
	e.do(t, "PUT", path("/sections/%d/scores", e.section), instr, map[string]any{"entries": []any{
		e.score(t, 0, "midterm", "regular", 60), e.score(t, 0, "final", "regular", 60)}}) // 60 → DC
	_, pub := e.do(t, "POST", path("/sections/%d/grades/publish", e.section), instr, nil)
	res := pub["results"].([]any)[0].(map[string]any)
	resultID, oldLetter := int64(res["result_id"].(float64)), res["letter"].(string)
	if oldLetter != "DC" {
		t.Fatalf("ilk not %s, beklenen DC", oldLetter)
	}
	if _, err := ProcessBatch(context.Background(), e.pool, 10); err != nil {
		t.Fatal(err)
	}

	// Talebi yalnızca o şubenin öğretim üyesi açabilir.
	url := path("/course-results/%d/corrections", resultID)
	if c, _ := e.do(t, "POST", url, e.token("other_instructor"), map[string]any{"new_letter": "BB", "reason": "x"}); c != 403 {
		t.Errorf("başka öğretim üyesi talebi: %d, beklenen 403", c)
	}
	code, body := e.do(t, "POST", url, instr, map[string]any{"new_letter": "BB", "reason": "Maddi hata"})
	if code != 201 {
		t.Fatalf("talep: %d %v", code, body)
	}
	corrID := int64(body["id"].(float64))
	if c, _ := e.do(t, "POST", url, instr, map[string]any{"new_letter": "BA", "reason": "ikinci"}); c != 409 {
		t.Errorf("ikinci bekleyen talep: %d, beklenen 409", c)
	}

	// Talebi açan kişi kendi talebini onaylayamaz (rolü gereği 403); öğrenci de onaylayamaz.
	dec := path("/grade-corrections/%d/decision", corrID)
	if c, _ := e.do(t, "POST", dec, instr, map[string]any{"approve": true}); c != 403 {
		t.Errorf("öğretim üyesi onayı: %d, beklenen 403", c)
	}
	if c, _ := e.do(t, "POST", dec, e.token(auth.RoleStudent), map[string]any{"approve": true}); c != 403 {
		t.Errorf("öğrenci onayı: %d, beklenen 403", c)
	}
	// Onaylanmadan not değişmemeli.
	if l := mustScan[string](t, e.pool, `SELECT letter FROM course_results WHERE id=$1`, resultID); l != "DC" {
		t.Errorf("onay öncesi not %s, beklenen DC", l)
	}

	// İkinci kişi (öğrenci işleri) onaylar.
	if c, b := e.do(t, "POST", dec, e.token(auth.RoleRegistrar), map[string]any{"approve": true}); c != 200 {
		t.Fatalf("onay: %d %v", c, b)
	}
	if l := mustScan[string](t, e.pool, `SELECT letter FROM course_results WHERE id=$1`, resultID); l != "BB" {
		t.Errorf("onay sonrası not %s, beklenen BB", l)
	}
	if c, _ := e.do(t, "POST", dec, e.token(auth.RoleRegistrar), map[string]any{"approve": true}); c != 409 {
		t.Errorf("tekrar onay: %d, beklenen 409", c)
	}

	// Tam geçmiş: eski/yeni not, talep eden ve onaylayan kayıtlı; ve değiştirilemez.
	if n := mustScan[int](t, e.pool,
		`SELECT count(*) FROM grade_history WHERE result_id=$1 AND old_letter='DC' AND new_letter='BB' AND requested_by<>approved_by`, resultID); n != 1 {
		t.Errorf("geçmiş satırı %d, beklenen 1", n)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE grade_history SET new_letter='AA'`); err == nil {
		t.Error("not geçmişi değiştirilebildi")
	}
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM grade_history`); err == nil {
		t.Error("not geçmişi silinebildi")
	}

	// Değişen nottan sonra GANO işçisi yeni ortalamayı yazar; tekrar çalışması sonucu değiştirmez.
	for i := 0; i < 3; i++ {
		if _, err := ProcessBatch(context.Background(), e.pool, 10); err != nil {
			t.Fatal(err)
		}
		if g := mustScan[float64](t, e.pool, `SELECT gano::float8 FROM students WHERE id=$1`, e.students[0]); g != 3 {
			t.Fatalf("tur %d: GANO %v, beklenen 3 (BB)", i, g)
		}
		// Aynı işi bilerek tekrar kuyruğa al (idempotans).
		if err := Enqueue(context.Background(), e.pool, []int64{e.students[0]}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRetakePolicyRecalculatesProgram(t *testing.T) {
	e := setup(t, 1)
	ctx := context.Background()
	// Aynı dersin iki denemesi: T2 döneminde BB (3.0), sonra T3 döneminde kötüleşen DC (1.5).
	t2 := mustScan[int64](t, e.pool, `INSERT INTO terms (code, name) VALUES ('T2', 'T2') RETURNING id`)
	t3 := mustScan[int64](t, e.pool, `INSERT INTO terms (code, name) VALUES ('T3', 'T3') RETURNING id`)
	mustExec(t, e.pool, `INSERT INTO course_results (student_id, course_id, term_id, letter, grade_point, passed) VALUES ($1,$2,$3,'BB',3.0,true)`, e.students[0], e.course, t2)
	mustExec(t, e.pool, `INSERT INTO course_results (student_id, course_id, term_id, letter, grade_point, passed) VALUES ($1,$2,$3,'DC',1.5,true)`, e.students[0], e.course, t3)

	gano := func() float64 {
		return mustScan[float64](t, e.pool, `SELECT gano::float8 FROM students WHERE id=$1`, e.students[0])
	}
	set := func(policy string) map[string]any {
		code, body := e.do(t, "PUT", path("/programs/%d/retake-policy", e.program), e.token(auth.RoleRegistrar), map[string]any{"policy": policy})
		if code != 200 {
			t.Fatalf("politika %s: %d %v", policy, code, body)
		}
		if _, err := ProcessBatch(ctx, e.pool, 100); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if body := set("best"); body["recalculating_students"].(float64) != 1 {
		t.Errorf("yeniden hesaplanan öğrenci: %v", body["recalculating_students"])
	}
	if g := gano(); g != 3 {
		t.Errorf("best politikası GANO %v, beklenen 3 (BB)", g)
	}
	set("last")
	if g := gano(); g != 1.5 {
		t.Errorf("last politikası GANO %v, beklenen 1.5 (DC)", g)
	}
	// Yetki: yalnızca öğrenci işleri politikayı değiştirebilir.
	if c, _ := e.do(t, "PUT", path("/programs/%d/retake-policy", e.program), e.token(auth.RoleDeptSecretary), map[string]any{"policy": "best"}); c != 403 {
		t.Errorf("bölüm sekreteri politika: %d, beklenen 403", c)
	}
}

func TestStudentResultsAccess(t *testing.T) {
	e := setup(t, 2)
	// 0. öğrencinin kullanıcısıyla giriş: kendi sonuçlarını görür, diğerininkini göremez.
	uid := mustScan[int64](t, e.pool, `SELECT user_id FROM students WHERE id=$1`, e.students[0])
	tok, _ := e.iss.Access(uid, auth.RoleStudent)
	if c, _ := e.do(t, "GET", path("/students/%d/results", e.students[0]), tok, nil); c != 200 {
		t.Errorf("kendi sonucu: %d, beklenen 200", c)
	}
	if c, _ := e.do(t, "GET", path("/students/%d/results", e.students[1]), tok, nil); c != 403 {
		t.Errorf("başkasının sonucu: %d, beklenen 403", c)
	}
	if c, _ := e.do(t, "GET", path("/students/%d/results", e.students[1]), e.token(auth.RoleRegistrar), nil); c != 200 {
		t.Errorf("öğrenci işleri: %d, beklenen 200", c)
	}
	if c, _ := e.do(t, "GET", path("/students/%d/results", e.students[1]), e.token(auth.RoleInstructor), nil); c != 403 {
		t.Errorf("öğretim üyesi: %d, beklenen 403", c)
	}
	// Öğretim üyesi yalnızca kendi şubesinin sonuçlarını görür.
	if c, _ := e.do(t, "GET", path("/sections/%d/results", e.section), e.token("other_instructor"), nil); c != 403 {
		t.Errorf("başka öğretim üyesi şube sonucu: %d, beklenen 403", c)
	}
	if c, _ := e.do(t, "GET", path("/sections/%d/results", e.section), e.token(auth.RoleInstructor), nil); c != 200 {
		t.Errorf("kendi şubesi: %d, beklenen 200", c)
	}
}
