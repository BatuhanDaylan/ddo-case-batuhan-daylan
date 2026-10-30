package timetable

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
	"github.com/batuhan-daylan/uninet/internal/testdb"
)

type env struct {
	pool   *pgxpool.Pool
	router http.Handler
	iss    *auth.Issuer
	term   int64
	reg    int64
	instr  []int64
	stu    int64
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

// setup: 6 şube (2 öğretim üyesi), 4 derslik; ilk 4 ders aynı yarıyılın zorunlu dersi (aynı grup).
func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	e := &env{pool: pool}
	user := func(email, role string) int64 {
		return scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ($1,'x',$1,$2) RETURNING id`, email, role)
	}
	e.reg, e.stu = user("r@t", "registrar"), user("s@t", "student")
	e.instr = []int64{user("i1@t", "instructor"), user("i2@t", "instructor")}

	f := scan[int64](t, pool, `INSERT INTO faculties (code,name) VALUES ('F','F') RETURNING id`)
	d := scan[int64](t, pool, `INSERT INTO departments (faculty_id,code,name) VALUES ($1,'D','D') RETURNING id`, f)
	prog := scan[int64](t, pool, `INSERT INTO programs (department_id,code,name,total_ects_required) VALUES ($1,'P','P',240) RETURNING id`, d)
	version := scan[int64](t, pool, `INSERT INTO curriculum_versions (program_id,effective_year) VALUES ($1,2024) RETURNING id`, prog)
	e.term = scan[int64](t, pool, `INSERT INTO terms (code,name) VALUES ('T','T') RETURNING id`)

	for i := 0; i < 6; i++ {
		kind := "lecture"
		if i == 5 {
			kind = "lab"
		}
		c := scan[int64](t, pool, `INSERT INTO courses (code,name,ects,weekly_hours,room_kind) VALUES ($1,$1,5,2,$2) RETURNING id`, fmt.Sprint("C", i), kind)
		exec(t, pool, `INSERT INTO sections (term_id,course_id,code,instructor_id,capacity) VALUES ($1,$2,'01',$3,30)`, e.term, c, e.instr[i%2])
		if i < 4 {
			exec(t, pool, `INSERT INTO curriculum_courses (curriculum_version_id,course_id,semester,kind) VALUES ($1,$2,1,'required')`, version, c)
		}
	}
	for _, r := range []struct {
		code string
		cap  int
		kind string
	}{{"R1", 40, "lecture"}, {"R2", 40, "lecture"}, {"R3", 60, "lecture"}, {"LAB1", 30, "lab"}} {
		exec(t, pool, `INSERT INTO rooms (code,capacity,kind) VALUES ($1,$2,$3)`, r.code, r.cap, r.kind)
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

func (e *env) solveURL() string { return fmt.Sprintf("/terms/%d/schedule/solve", e.term) }

func TestSolveApplyProducesConflictFreeSchedule(t *testing.T) {
	e := setup(t)
	code, body := e.do("POST", e.solveURL(), e.reg, auth.RoleRegistrar, map[string]any{"apply": true, "time_limit_seconds": 10})
	if code != 200 || body["status"] != "solved" || body["applied"] != true {
		t.Fatalf("çözüm: %d %v", code, body)
	}
	if n := scan[int](t, e.pool, `SELECT count(*) FROM section_meetings WHERE room_id IS NOT NULL`); n != 6 {
		t.Fatalf("yazılan oturum %d, beklenen 6", n)
	}

	// Bağımsız SQL denetimleri (çözücüden ve Go denetçisinden bağımsız).
	overlap := `a.id < b.id AND a.day = b.day AND a.start_min < b.end_min AND b.start_min < a.end_min`
	if n := scan[int](t, e.pool, `SELECT count(*) FROM section_meetings a JOIN section_meetings b ON `+overlap+` AND a.room_id = b.room_id`); n != 0 {
		t.Errorf("aynı derslikte %d çakışma", n)
	}
	if n := scan[int](t, e.pool, `SELECT count(*) FROM section_meetings a JOIN sections sa ON sa.id=a.section_id
		JOIN section_meetings b ON `+overlap+` JOIN sections sb ON sb.id=b.section_id AND sa.instructor_id = sb.instructor_id`); n != 0 {
		t.Errorf("aynı öğretim üyesinde %d çakışma", n)
	}
	// İlk 4 ders aynı yarıyılın zorunlu dersi: birbiriyle çakışmamalı.
	if n := scan[int](t, e.pool, `SELECT count(*) FROM section_meetings a JOIN sections sa ON sa.id=a.section_id JOIN courses ca ON ca.id=sa.course_id
		JOIN section_meetings b ON `+overlap+` JOIN sections sb ON sb.id=b.section_id JOIN courses cb ON cb.id=sb.course_id
		WHERE ca.code IN ('C0','C1','C2','C3') AND cb.code IN ('C0','C1','C2','C3')`); n != 0 {
		t.Errorf("aynı yarıyıl zorunlu derslerinde %d çakışma", n)
	}
	// Lab dersi yalnızca laboratuvara yerleşmiş olmalı.
	if kind := scan[string](t, e.pool, `SELECT r.kind FROM section_meetings m JOIN rooms r ON r.id=m.room_id
		JOIN sections s ON s.id=m.section_id JOIN courses c ON c.id=s.course_id WHERE c.code='C5'`); kind != "lab" {
		t.Errorf("lab dersi %s dersliğine yerleşti", kind)
	}
	if n := scan[int](t, e.pool, `SELECT count(*) FROM audit_log WHERE action='term.schedule_applied'`); n != 1 {
		t.Errorf("program uygulaması denetime yazılmadı")
	}

	// Program görüntüleme (keyset sayfalı) — her rol görebilir.
	code, page := e.do("GET", fmt.Sprintf("/terms/%d/schedule?limit=4", e.term), e.stu, auth.RoleStudent, nil)
	if code != 200 || len(page["items"].([]any)) != 4 || page["next_after"] == nil {
		t.Errorf("sayfa: %d %v", code, page)
	}
}

func TestSolveReportsNamedConflictsAndDoesNotApply(t *testing.T) {
	e := setup(t)
	// Önce geçerli bir program uygulansın.
	if code, b := e.do("POST", e.solveURL(), e.reg, auth.RoleRegistrar, map[string]any{"apply": true}); code != 200 || b["status"] != "solved" {
		t.Fatalf("ilk çözüm: %d %v", code, b)
	}
	before := scan[string](t, e.pool, `SELECT string_agg(id::text || ':' || day || ':' || start_min || ':' || room_id, ',' ORDER BY id) FROM section_meetings`)

	// Öğretim üyesi 1 bütün hafta müsait değil.
	var slots []map[string]int
	for d := 1; d <= 5; d++ {
		for s := 0; s < 8; s++ {
			slots = append(slots, map[string]int{"day": d, "slot": s})
		}
	}
	if code, b := e.do("PUT", fmt.Sprintf("/instructors/%d/unavailability", e.instr[0]), e.reg, auth.RoleRegistrar, map[string]any{"slots": slots}); code != 200 {
		t.Fatalf("müsaitlik: %d %v", code, b)
	}
	code, body := e.do("POST", e.solveURL(), e.reg, auth.RoleRegistrar, map[string]any{"apply": true})
	if code != 200 || body["status"] != "infeasible" || body["applied"] != false {
		t.Fatalf("çözümsüz bekleniyordu: %d %v", code, body)
	}
	found := false
	for _, c := range body["conflicts"].([]any) {
		if c.(map[string]any)["kind"] == "INSTRUCTOR_UNAVAILABLE" {
			found = true
		}
	}
	if !found {
		t.Errorf("öğretim üyesi müsaitlik çatışması raporlanmadı: %v", body["conflicts"])
	}
	// Çözümsüzken mevcut program bozulmamalı.
	after := scan[string](t, e.pool, `SELECT string_agg(id::text || ':' || day || ':' || start_min || ':' || room_id, ',' ORDER BY id) FROM section_meetings`)
	if before != after {
		t.Error("çözümsüz sonuç mevcut programı değiştirdi")
	}

	// Müsaitlik temizlenip derslikler küçültülünce uygun derslik yok çatışması adlandırılır.
	exec(t, e.pool, `DELETE FROM instructor_unavailable`)
	exec(t, e.pool, `UPDATE rooms SET capacity = 5`)
	_, body = e.do("POST", e.solveURL(), e.reg, auth.RoleRegistrar, nil)
	kindsSeen := map[any]bool{}
	for _, c := range body["conflicts"].([]any) {
		kindsSeen[c.(map[string]any)["kind"]] = true
	}
	if body["status"] != "infeasible" || !kindsSeen["NO_ROOM"] {
		t.Errorf("NO_ROOM bekleniyordu: %v", body)
	}
}

func TestTimetableAccessAndValidation(t *testing.T) {
	e := setup(t)
	if code, _ := e.do("POST", e.solveURL(), e.stu, auth.RoleStudent, nil); code != 403 {
		t.Errorf("öğrenci çözdü: %d", code)
	}
	if code, _ := e.do("POST", e.solveURL(), e.instr[0], auth.RoleInstructor, nil); code != 403 {
		t.Errorf("öğretim üyesi çözdü: %d", code)
	}
	if code, _ := e.do("POST", e.solveURL(), e.reg, auth.RoleRegistrar, map[string]any{"days": 20, "slots_per_day": 8, "time_limit_seconds": 5}); code != 400 {
		t.Errorf("geçersiz ızgara: %d", code)
	}
	if code, _ := e.do("POST", e.solveURL(), e.reg, auth.RoleRegistrar, map[string]any{"days": 5, "slots_per_day": 8, "time_limit_seconds": 9999}); code != 400 {
		t.Errorf("geçersiz süre: %d", code)
	}
	if code, _ := e.do("POST", "/terms/99999/schedule/solve", e.reg, auth.RoleRegistrar, nil); code != 404 {
		t.Errorf("olmayan dönem: %d", code)
	}
	if code, _ := e.do("PUT", fmt.Sprintf("/instructors/%d/unavailability", e.stu), e.reg, auth.RoleRegistrar, map[string]any{"slots": []any{}}); code != 404 {
		t.Errorf("öğrenci için müsaitlik: %d, beklenen 404", code)
	}
	if code, _ := e.do("PUT", fmt.Sprintf("/instructors/%d/unavailability", e.instr[0]), e.reg, auth.RoleRegistrar, map[string]any{"slots": []any{map[string]int{"day": 9, "slot": 0}}}); code != 400 {
		t.Errorf("geçersiz gün: %d", code)
	}
	if code, b := e.do("POST", "/rooms", e.reg, auth.RoleRegistrar, map[string]any{"code": "YENI", "capacity": 25, "kind": "lab"}); code != 201 {
		t.Errorf("derslik oluşturma: %d %v", code, b)
	}
	if code, _ := e.do("POST", "/rooms", e.reg, auth.RoleRegistrar, map[string]any{"code": "YENI", "capacity": 25, "kind": "lab"}); code != 409 {
		t.Errorf("aynı kodlu derslik: %d, beklenen 409", code)
	}
	if code, _ := e.do("POST", "/rooms", e.stu, auth.RoleStudent, map[string]any{"code": "X", "capacity": 25, "kind": "lab"}); code != 403 {
		t.Errorf("öğrenci derslik oluşturdu: %d", code)
	}
}
