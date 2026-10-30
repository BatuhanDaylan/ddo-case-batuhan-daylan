package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/testdb"
)

type env struct {
	pool     *pgxpool.Pool
	router   http.Handler
	iss      *auth.Issuer
	students []int64
	users    []int64 // students ile aynı sırada kullanıcı kimlikleri
	registr  int64
	advisor  int64
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
	e := &env{pool: pool}
	e.registr = scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ('r@t','x','Reg','registrar') RETURNING id`)
	e.advisor = scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ('a@t','x','Adv','advisor') RETURNING id`)
	f := scan[int64](t, pool, `INSERT INTO faculties (code,name) VALUES ('F','Mühendislik Fakültesi') RETURNING id`)
	d := scan[int64](t, pool, `INSERT INTO departments (faculty_id,code,name) VALUES ($1,'D','Bilgisayar Mühendisliği') RETURNING id`, f)
	prog := scan[int64](t, pool, `INSERT INTO programs (department_id,code,name,total_ects_required) VALUES ($1,'P','BM Lisans',240) RETURNING id`, d)
	term := scan[int64](t, pool, `INSERT INTO terms (code,name) VALUES ('2025-GUZ','Güz') RETURNING id`)
	c1 := scan[int64](t, pool, `INSERT INTO courses (code,name,ects) VALUES ('BM101','Programlamaya Giriş',6) RETURNING id`)
	c2 := scan[int64](t, pool, `INSERT INTO courses (code,name,ects) VALUES ('MAT101','Matematik I',6) RETURNING id`)
	for i, name := range []string{"Şule Çağlar", "Öznur Işık"} { // Türkçe karakterler PDF'e basılabilmeli
		uid := scan[int64](t, pool, `INSERT INTO users (email,password_hash,full_name,role) VALUES ($1,'x',$2,'student') RETURNING id`, fmt.Sprintf("s%d@t", i), name)
		var adv any
		if i == 0 {
			adv = e.advisor
		}
		sid := scan[int64](t, pool, `INSERT INTO students (user_id,student_no,program_id,entry_year,advisor_id) VALUES ($1,$2,$3,2024,$4) RETURNING id`,
			uid, fmt.Sprintf("N%d", i), prog, adv)
		e.students, e.users = append(e.students, sid), append(e.users, uid)
	}
	exec(t, pool, `INSERT INTO course_results (student_id,course_id,term_id,letter,grade_point,passed) VALUES ($1,$2,$3,'AA',4.0,true)`, e.students[0], c1, term)
	exec(t, pool, `INSERT INTO course_results (student_id,course_id,term_id,letter,grade_point,passed) VALUES ($1,$2,$3,'FF',0,false)`, e.students[0], c2, term)

	signer, _ := NewSigner(testKey)
	h := NewHandler(pool, NewService(pool, signer, "http://uninet.test"))
	e.iss = auth.NewIssuer("integration-test-secret-integration-test", time.Hour)
	r := chi.NewRouter()
	h.PublicRoutes(r)
	r.Group(func(r chi.Router) { r.Use(e.iss.Authenticate); h.Routes(r) })
	e.router = r
	return e
}

func (e *env) req(method, path string, userID int64, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(nil))
	if userID != 0 {
		tok, _ := e.iss.Access(userID, role)
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *env) issue(t *testing.T) Issued {
	t.Helper()
	rec := e.req("POST", fmt.Sprintf("/students/%d/transcripts", e.students[0]), e.users[0], auth.RoleStudent)
	if rec.Code != 201 {
		t.Fatalf("belge düzenleme: %d %s", rec.Code, rec.Body)
	}
	var out Issued
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIssueVerifyAndPDF(t *testing.T) {
	e := setup(t)
	doc := e.issue(t)
	if !strings.HasPrefix(doc.VerifyURL, "http://uninet.test/verify/") || !strings.Contains(doc.VerifyURL, doc.Code) {
		t.Fatalf("doğrulama adresi: %s", doc.VerifyURL)
	}

	// Doğrulama herkese açıktır (kimlik bilgisi YOK) ve belge özetini döner.
	rec := e.req("GET", "/verify/"+doc.Code, 0, "")
	if rec.Code != 200 {
		t.Fatalf("doğrulama: %d %s", rec.Code, rec.Body)
	}
	var v VerifyResult
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	// 6 AKTS AA (4.0) ve 6 AKTS FF (0.0) → (24+0)/12 = 2.00
	if !v.Valid || v.FullName != "Şule Çağlar" || v.GANO != 2 || v.Courses != 2 || v.DocumentID != doc.ID {
		t.Errorf("beklenmeyen doğrulama sonucu: %+v", v)
	}

	// PDF: gerçek bir PDF, boş değil, Türkçe karakterlerle üretilebilmiş.
	pdf := e.req("GET", doc.PDFURL, e.users[0], auth.RoleStudent)
	if pdf.Code != 200 || pdf.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("pdf: %d %s", pdf.Code, pdf.Header().Get("Content-Type"))
	}
	if b := pdf.Body.Bytes(); !bytes.HasPrefix(b, []byte("%PDF-")) || len(b) < 5000 || !bytes.Contains(b[len(b)-64:], []byte("%%EOF")) {
		t.Errorf("geçerli PDF değil: %d bayt", len(b))
	}
}

func TestVerifyRejectsForgeryAndTampering(t *testing.T) {
	e := setup(t)
	doc := e.issue(t)
	ctx := context.Background()

	for name, code := range map[string]string{
		"çöp":                "çöp",
		"olmayan belge":      "9999." + strings.SplitN(doc.Code, ".", 2)[1],
		"başka belge imzası": "1." + strings.Repeat("A", 43),
		"imza bozuk":         doc.Code[:len(doc.Code)-2] + "AA",
	} {
		if rec := e.req("GET", "/verify/"+code, 0, ""); rec.Code != 404 {
			t.Errorf("%s: %d, beklenen 404", name, rec.Code)
		}
	}

	// Normalde belge tetikleyiciyle değiştirilemez.
	if _, err := e.pool.Exec(ctx, `UPDATE documents SET snapshot = replace(snapshot, '"gano":2', '"gano":4')`); err == nil {
		t.Error("belge içeriği değiştirilebildi")
	}
	// Tetikleyiciler devre dışı bırakılsa BİLE (yetkili kurcalama) doğrulama başarısız olmalı.
	exec(t, e.pool, `ALTER TABLE documents DISABLE TRIGGER USER`)
	exec(t, e.pool, `UPDATE documents SET snapshot = replace(snapshot, '"gano":2', '"gano":4')`)
	if rec := e.req("GET", "/verify/"+doc.Code, 0, ""); rec.Code != 404 {
		t.Errorf("içeriği kurcalanmış belge doğrulandı: %d", rec.Code)
	}
	// Saldırgan özeti de güncellese imza (anahtarı bilmediği için) yine tutmaz.
	exec(t, e.pool, `UPDATE documents SET snapshot_sha256 = encode(sha256(convert_to(snapshot,'UTF8')),'hex')`)
	if rec := e.req("GET", "/verify/"+doc.Code, 0, ""); rec.Code != 404 {
		t.Errorf("özeti de güncellenmiş belge doğrulandı: %d", rec.Code)
	}
}

func TestDocumentAccessControl(t *testing.T) {
	e := setup(t)
	doc := e.issue(t) // 0. öğrencinin belgesi

	cases := []struct {
		name   string
		user   int64
		role   string
		method string
		path   string
		want   int
	}{
		{"başka öğrenci pdf indiremez", e.users[1], auth.RoleStudent, "GET", doc.PDFURL, 403},
		{"danışman (danışanı) indirebilir", e.advisor, auth.RoleAdvisor, "GET", doc.PDFURL, 200},
		{"öğrenci işleri indirebilir", e.registr, auth.RoleRegistrar, "GET", doc.PDFURL, 200},
		{"öğretim üyesi indiremez", e.registr, auth.RoleInstructor, "GET", doc.PDFURL, 403},
		{"kimliksiz pdf", 0, "", "GET", doc.PDFURL, 401},
		{"başkası adına belge düzenleme", e.users[1], auth.RoleStudent, "POST", fmt.Sprintf("/students/%d/transcripts", e.students[0]), 403},
		{"danışanı olmayan öğrenci için danışman", e.advisor, auth.RoleAdvisor, "POST", fmt.Sprintf("/students/%d/transcripts", e.students[1]), 403},
		{"olmayan belge", e.registr, auth.RoleRegistrar, "GET", "/documents/99999/pdf", 404},
	}
	for _, c := range cases {
		if rec := e.req(c.method, c.path, c.user, c.role); rec.Code != c.want {
			t.Errorf("%s: %d, beklenen %d", c.name, rec.Code, c.want)
		}
	}

	// Belge düzenleme denetim kaydı bırakmış olmalı.
	if n := scan[int](t, e.pool, `SELECT count(*) FROM audit_log WHERE action = 'document.issued'`); n < 1 {
		t.Errorf("belge düzenleme denetime yazılmadı")
	}
}

func TestRenderTranscriptManyRowsPaginates(t *testing.T) {
	s := Snapshot{Kind: "transcript", IssuedAt: time.Unix(1_700_000_000, 0).UTC(), StudentNo: "N1", FullName: "İğdır Şeker",
		Faculty: "F", Department: "D", Program: "P", EntryYear: 2021}
	for i := 0; i < 120; i++ {
		s.Results = append(s.Results, ResultRow{Term: "T", Code: fmt.Sprintf("C%03d", i), Name: "Çok uzun bir ders adı ğüşıöç", ECTS: 5, Letter: "BA", Point: 3.5, Passed: true})
	}
	b, err := RenderTranscript(s, "1.abc", "http://x/verify/1.abc")
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(b, []byte("/Type /Page\n")) + bytes.Count(b, []byte("/Type /Page ")); n < 2 {
		t.Errorf("çok satırlı transkript tek sayfaya sığdırıldı (sayfa nesnesi=%d)", n)
	}
}
