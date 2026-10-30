package registration

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
)

type apprEnv struct {
	pool     *pgxpool.Pool
	router   http.Handler
	iss      *auth.Issuer
	section  int64
	students []int64
	advisor  int64
	other    int64 // danışan olmayan başka bir danışman
	registr  int64
}

func newApprEnv(t *testing.T, students, capacity int) *apprEnv {
	t.Helper()
	pool := newTestPool(t)
	section, ids := fixture(t, pool, students, capacity)
	ctx := context.Background()
	e := &apprEnv{pool: pool, section: section, students: ids}
	mk := func(email, role string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO users (email,password_hash,full_name,role) VALUES ($1,'x',$1,$2) RETURNING id`, email, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.advisor, e.other, e.registr = mk("adv@t", "advisor"), mk("adv2@t", "advisor"), mk("reg@t", "registrar")
	if _, err := pool.Exec(ctx, `UPDATE students SET advisor_id = $1`, e.advisor); err != nil {
		t.Fatal(err)
	}
	e.iss = auth.NewIssuer("integration-test-secret-integration-test", time.Hour)
	h := NewHandler(pool)
	r := chi.NewRouter()
	r.Use(e.iss.Authenticate)
	h.Routes(r)
	h.ApprovalRoutes(r)
	e.router = r
	return e
}

func (e *apprEnv) do(t *testing.T, method, path string, user int64, role string, body any) (int, map[string]any) {
	t.Helper()
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

func TestAdvisorApprovalAndRejectionPromotesWaitlist(t *testing.T) {
	e := newApprEnv(t, 3, 2) // kontenjan 2, 3 öğrenci: üçüncü bekleme listesinde
	ctx := context.Background()
	var enr []int64
	for _, sid := range e.students {
		res, err := enrollOnce(ctx, e.pool, sid, e.section)
		if err != nil {
			t.Fatal(err)
		}
		enr = append(enr, res.EnrollmentID)
	}

	// Danışman yalnızca bekleyen kayıtları görür (3 kayıt, hepsi pending).
	code, page := e.do(t, "GET", "/advisor/enrollments?status=pending", e.advisor, auth.RoleAdvisor, nil)
	if code != 200 || len(page["items"].([]any)) != 3 {
		t.Fatalf("danışman listesi: %d %v", code, page)
	}
	// Danışanı olmayan danışman hiçbirini görmez ve karar veremez (404).
	if _, p := e.do(t, "GET", "/advisor/enrollments", e.other, auth.RoleAdvisor, nil); len(p["items"].([]any)) != 0 {
		t.Errorf("başka danışman kayıtları gördü: %v", p)
	}
	decide := "/enrollments/%d/advisor-decision"
	if c, _ := e.do(t, "POST", path(decide, enr[0]), e.other, auth.RoleAdvisor, map[string]any{"approve": true}); c != 404 {
		t.Errorf("başkasının danışanı için karar: %d, beklenen 404", c)
	}
	// Öğrenci/öğrenci işleri danışman kararı veremez (rol).
	if c, _ := e.do(t, "POST", path(decide, enr[0]), e.registr, auth.RoleRegistrar, map[string]any{"approve": true}); c != 403 {
		t.Errorf("öğrenci işleri danışman kararı verdi: %d", c)
	}

	// Onay.
	if c, b := e.do(t, "POST", path(decide, enr[0]), e.advisor, auth.RoleAdvisor, map[string]any{"approve": true}); c != 200 || b["advisor_status"] != "approved" {
		t.Fatalf("onay: %d %v", c, b)
	}
	if c, _ := e.do(t, "POST", path(decide, enr[0]), e.advisor, auth.RoleAdvisor, map[string]any{"approve": false}); c != 409 {
		t.Errorf("verilmiş karar yeniden değiştirildi: %d, beklenen 409", c)
	}

	// Ret: kayıt düşer, koltuk AYNI işlemde bekleme listesinin ilk kişisine geçer.
	c, b := e.do(t, "POST", path(decide, enr[1]), e.advisor, auth.RoleAdvisor, map[string]any{"approve": false})
	if c != 200 || b["advisor_status"] != "rejected" || int64(b["promoted_enrollment_id"].(float64)) != enr[2] {
		t.Fatalf("ret: %d %v (terfi beklenen %d)", c, b, enr[2])
	}
	if s := count(t, e.pool, `SELECT count(*) FROM enrollments WHERE id=$1 AND status='dropped'`, enr[1]); s != 1 {
		t.Error("reddedilen kayıt düşmedi")
	}
	if s := count(t, e.pool, `SELECT count(*) FROM enrollments WHERE id=$1 AND status='enrolled'`, enr[2]); s != 1 {
		t.Error("bekleme listesinden terfi olmadı")
	}
	if n := count(t, e.pool, `SELECT enrolled_count FROM sections WHERE id=$1`, e.section); n != 2 {
		t.Errorf("enrolled_count %d, beklenen 2", n)
	}
}

func path(format string, id int64) string { return fmt.Sprintf(format, id) }

func TestDropDeadlineBlocksStudentButNotAdvisor(t *testing.T) {
	e := newApprEnv(t, 2, 5)
	ctx := context.Background()
	res, err := enrollOnce(ctx, e.pool, e.students[0], e.section)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := enrollOnce(ctx, e.pool, e.students[1], e.section)
	if err != nil {
		t.Fatal(err)
	}
	termID := count(t, e.pool, `SELECT term_id FROM sections WHERE id=$1`, e.section)

	// Süre dolmuş: öğrenci bırakamaz (DropTx doğrudan), ama danışman reddi idari işlemdir.
	if _, err := e.pool.Exec(ctx, `UPDATE terms SET drop_deadline = now() - interval '1 day' WHERE id=$1`, termID); err != nil {
		t.Fatal(err)
	}
	tx, _ := e.pool.Begin(ctx)
	if _, err := DropTx(ctx, tx, e.students[0], res.EnrollmentID); err != ErrDropPeriodOver {
		t.Errorf("süre dolduktan sonra bırakma: %v, beklenen ErrDropPeriodOver", err)
	}
	_ = tx.Rollback(ctx)
	if c, b := e.do(t, "POST", path("/enrollments/%d/advisor-decision", res2.EnrollmentID), e.advisor, auth.RoleAdvisor, map[string]any{"approve": false}); c != 200 {
		t.Errorf("süre dolmuşken danışman reddi: %d %v", c, b)
	}

	// Süre sınırı kaldırılınca (NULL) öğrenci bırakabilir. Yönetici ucu: yetki ve doğrulama.
	if c, _ := e.do(t, "PUT", path("/terms/%d/drop-deadline", int64(termID)), e.advisor, auth.RoleAdvisor, map[string]any{"deadline": nil}); c != 403 {
		t.Errorf("danışman süre değiştirdi: %d, beklenen 403", c)
	}
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	if c, b := e.do(t, "PUT", path("/terms/%d/drop-deadline", int64(termID)), e.registr, auth.RoleRegistrar, map[string]any{"deadline": future}); c != 200 {
		t.Fatalf("süre ayarı: %d %v", c, b)
	}
	tx, _ = e.pool.Begin(ctx)
	if _, err := DropTx(ctx, tx, e.students[0], res.EnrollmentID); err != nil {
		t.Errorf("süre dolmamışken bırakma: %v", err)
	}
	_ = tx.Commit(ctx)
	if n := count(t, e.pool, `SELECT count(*) FROM audit_log WHERE action='term.drop_deadline_changed'`); n != 1 {
		t.Errorf("süre değişikliği denetime yazılmadı")
	}
}
