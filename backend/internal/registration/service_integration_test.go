package registration

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/db"
	"github.com/batuhan-daylan/uninet/internal/idempotency"
)

// Bu testler gerçek PostgreSQL ister. TEST_DATABASE_URL yoksa atlanır. Her test kendi geçici
// veritabanını oluşturur ve sonunda siler; geliştirme verisine dokunmaz.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil; entegrasyon testi atlandı")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("uninet_test_%d", rand.Int64N(1<<40))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(base)
	u.Path = "/" + name
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 50
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	if err := db.Migrate(pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// fixture: n öğrenci ve capacity kontenjanlı tek bir açık şube kurar.
func fixture(t *testing.T, pool *pgxpool.Pool, students, capacity int) (sectionID int64, studentIDs []int64) {
	t.Helper()
	ctx := context.Background()
	var facultyID, deptID, programID, courseID, termID int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO faculties (code, name) VALUES ('F', 'F') RETURNING id`).Scan(&facultyID))
	must(pool.QueryRow(ctx, `INSERT INTO departments (faculty_id, code, name) VALUES ($1, 'D', 'D') RETURNING id`, facultyID).Scan(&deptID))
	must(pool.QueryRow(ctx, `INSERT INTO programs (department_id, code, name, total_ects_required) VALUES ($1, 'P', 'P', 240) RETURNING id`, deptID).Scan(&programID))
	must(pool.QueryRow(ctx, `INSERT INTO courses (code, name, ects) VALUES ('C1', 'C', 5) RETURNING id`).Scan(&courseID))
	must(pool.QueryRow(ctx, `INSERT INTO terms (code, name, registration_open) VALUES ('T', 'T', true) RETURNING id`).Scan(&termID))
	must(pool.QueryRow(ctx,
		`INSERT INTO sections (term_id, course_id, code, capacity) VALUES ($1, $2, '01', $3) RETURNING id`,
		termID, courseID, capacity).Scan(&sectionID))
	_, err := pool.Exec(ctx,
		`INSERT INTO users (email, password_hash, full_name, role)
		 SELECT 's' || g || '@t.test', 'x', 'S' || g, 'student' FROM generate_series(1, $1) g`, students)
	must(err)
	_, err = pool.Exec(ctx,
		`INSERT INTO students (user_id, student_no, program_id, entry_year)
		 SELECT id, 'N' || id, $1, 2024 FROM users`, programID)
	must(err)
	rows, err := pool.Query(ctx, `SELECT id FROM students ORDER BY id`)
	must(err)
	defer rows.Close()
	for rows.Next() {
		var id int64
		must(rows.Scan(&id))
		studentIDs = append(studentIDs, id)
	}
	return sectionID, studentIDs
}

func enrollOnce(ctx context.Context, pool *pgxpool.Pool, studentID, sectionID int64) (EnrollResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return EnrollResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := EnrollTx(ctx, tx, studentID, sectionID)
	if err != nil {
		return EnrollResult{}, err
	}
	return res, tx.Commit(ctx)
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// T-1 kanıtı: 500 eşzamanlı istek, kontenjan 40. Aşım olmamalı, fazlası bekleme listesine girmeli.
func TestConcurrentEnrollNeverExceedsCapacity(t *testing.T) {
	const students, capacity = 500, 40
	pool := newTestPool(t)
	sectionID, ids := fixture(t, pool, students, capacity)
	ctx := context.Background()

	results := make([]EnrollResult, len(ids))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, sid := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // hepsi aynı anda ateşlensin
			res, err := enrollOnce(ctx, pool, sid, sectionID)
			if err != nil {
				t.Errorf("öğrenci %d: %v", sid, err)
			}
			results[i] = res
		}()
	}
	close(start)
	wg.Wait()

	if got := count(t, pool, `SELECT count(*) FROM enrollments WHERE section_id=$1 AND status='enrolled'`, sectionID); got != capacity {
		t.Errorf("enrolled satır sayısı %d, beklenen %d", got, capacity)
	}
	if got := count(t, pool, `SELECT enrolled_count FROM sections WHERE id=$1`, sectionID); got != capacity {
		t.Errorf("sections.enrolled_count %d, beklenen %d", got, capacity)
	}
	if got := count(t, pool, `SELECT count(*) FROM enrollments WHERE section_id=$1 AND status='waitlisted'`, sectionID); got != students-capacity {
		t.Errorf("bekleme listesi %d, beklenen %d", got, students-capacity)
	}

	// Adalet: koltuk alan her satırın id'si, bekleyen her satırın id'sinden küçük olmalı.
	if bad := count(t, pool,
		`SELECT count(*) FROM enrollments w JOIN enrollments e ON e.section_id = w.section_id
		  WHERE w.section_id=$1 AND w.status='waitlisted' AND e.status='enrolled' AND e.id > w.id`, sectionID); bad != 0 {
		t.Errorf("sıra ihlali: %d çift", bad)
	}

	// Bekleme sırası 1..460 arasında tekrarsız olmalı.
	seen := map[int]bool{}
	for _, r := range results {
		if r.WaitlistPosition != nil {
			seen[*r.WaitlistPosition] = true
		}
	}
	if len(seen) != students-capacity {
		t.Errorf("benzersiz bekleme sırası %d, beklenen %d", len(seen), students-capacity)
	}
}

// Koltuk boşalınca bekleme listesinin ilk sırasındakiler, sırayla ve otomatik atanmalı.
func TestDropPromotesWaitlistInOrder(t *testing.T) {
	const students, capacity, drops = 60, 10, 4
	pool := newTestPool(t)
	sectionID, ids := fixture(t, pool, students, capacity)
	ctx := context.Background()

	// Sıralı kayıt: ilk 10 koltuk, kalanı bekleme listesi (id sırasıyla).
	enrollIDs := make([]int64, len(ids))
	for i, sid := range ids {
		res, err := enrollOnce(ctx, pool, sid, sectionID)
		if err != nil {
			t.Fatal(err)
		}
		enrollIDs[i] = res.EnrollmentID
	}

	// İlk `drops` koltuk sahibi aynı anda bırakır.
	var wg sync.WaitGroup
	for i := 0; i < drops; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := DropTx(ctx, tx, ids[i], enrollIDs[i]); err != nil {
				t.Error(err)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if got := count(t, pool, `SELECT enrolled_count FROM sections WHERE id=$1`, sectionID); got != capacity {
		t.Errorf("enrolled_count %d, beklenen %d (koltuk el değiştirdi, azalmamalı)", got, capacity)
	}
	if got := count(t, pool, `SELECT count(*) FROM enrollments WHERE section_id=$1 AND status='enrolled'`, sectionID); got != capacity {
		t.Errorf("enrolled satırlar %d, beklenen %d", got, capacity)
	}
	// Terfi edenler tam olarak bekleme listesinin ilk `drops` kişisi (kayıt sırası: indeks 10..13).
	for i := capacity; i < capacity+drops; i++ {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM enrollments WHERE id=$1`, enrollIDs[i]).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != StatusEnrolled {
			t.Errorf("bekleme sırası %d olan kayıt %q, beklenen enrolled", i-capacity+1, status)
		}
	}
	var next string
	if err := pool.QueryRow(ctx, `SELECT status FROM enrollments WHERE id=$1`, enrollIDs[capacity+drops]).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != StatusWaitlisted {
		t.Errorf("sıradaki kayıt %q, beklenen waitlisted", next)
	}

	// Bekleme listesi boşken bırakma, sayacı düşürmeli.
	pool2 := newTestPool(t)
	sec2, ids2 := fixture(t, pool2, 3, 3)
	var firstEnroll int64
	for i, sid := range ids2 {
		res, err := enrollOnce(ctx, pool2, sid, sec2)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstEnroll = res.EnrollmentID
		}
	}
	tx, _ := pool2.Begin(ctx)
	if _, err := DropTx(ctx, tx, ids2[0], firstEnroll); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(ctx)
	if got := count(t, pool2, `SELECT enrolled_count FROM sections WHERE id=$1`, sec2); got != 2 {
		t.Errorf("enrolled_count %d, beklenen 2", got)
	}
}

func TestEnrollRules(t *testing.T) {
	pool := newTestPool(t)
	sectionID, ids := fixture(t, pool, 2, 5)
	ctx := context.Background()

	if _, err := enrollOnce(ctx, pool, ids[0], sectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollOnce(ctx, pool, ids[0], sectionID); err != ErrAlreadyRegistered {
		t.Errorf("çift kayıt: %v, beklenen ErrAlreadyRegistered", err)
	}
	if _, err := enrollOnce(ctx, pool, ids[1], 999999); err != ErrSectionNotFound {
		t.Errorf("olmayan şube: %v, beklenen ErrSectionNotFound", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE terms SET registration_open = false`); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollOnce(ctx, pool, ids[1], sectionID); err != ErrRegistrationClosed {
		t.Errorf("kapalı kayıt: %v, beklenen ErrRegistrationClosed", err)
	}
	// Başkasının kaydını bırakamaz.
	tx, _ := pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	var someone int64
	_ = pool.QueryRow(ctx, `SELECT id FROM enrollments LIMIT 1`).Scan(&someone)
	if _, err := DropTx(ctx, tx, ids[1], someone); err != ErrEnrollmentNotFound {
		t.Errorf("başkasının kaydı: %v, beklenen ErrEnrollmentNotFound", err)
	}
}

// Aynı Idempotency-Key ile eşzamanlı 20 istek: iş yalnızca bir kez yapılmalı, hepsi aynı yanıtı almalı.
func TestIdempotencyConcurrentSameKey(t *testing.T) {
	pool := newTestPool(t)
	sectionID, ids := fixture(t, pool, 1, 5)
	ctx := context.Background()
	var userID int64
	if err := pool.QueryRow(ctx, `SELECT user_id FROM students WHERE id=$1`, ids[0]).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	bodies := make([]string, 20)
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := idempotency.Do(ctx, pool, userID, "anahtar-1", "POST /x", func(tx pgx.Tx) (int, []byte, error) {
				runs.Add(1)
				out, err := EnrollTx(ctx, tx, ids[0], sectionID)
				if err != nil {
					return 0, nil, err
				}
				return 201, jsonBody(out), nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			bodies[i] = string(res.Body)
		}()
	}
	wg.Wait()

	if runs.Load() != 1 {
		t.Errorf("iş %d kez çalıştı, beklenen 1", runs.Load())
	}
	for i := range bodies {
		if bodies[i] != bodies[0] || bodies[i] == "" {
			t.Fatalf("yanıtlar tutarsız: %q vs %q", bodies[i], bodies[0])
		}
	}
	if got := count(t, pool, `SELECT count(*) FROM enrollments`); got != 1 {
		t.Errorf("kayıt sayısı %d, beklenen 1", got)
	}

	// Aynı anahtar farklı istek için kullanılırsa reddedilmeli.
	_, err := idempotency.Do(ctx, pool, userID, "anahtar-1", "POST /baska", func(pgx.Tx) (int, []byte, error) { return 200, nil, nil })
	if err != idempotency.ErrKeyReuse {
		t.Errorf("anahtar yeniden kullanımı: %v, beklenen ErrKeyReuse", err)
	}
}

// addSection mevcut açık döneme yeni bir ders + 01 şubesi ekler.
func addSection(t *testing.T, pool *pgxpool.Pool, code string, ects int) (sectionID, courseID int64) {
	t.Helper()
	ctx := context.Background()
	var termID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM terms LIMIT 1`).Scan(&termID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO courses (code, name, ects) VALUES ($1, $1, $2) RETURNING id`, code, ects).Scan(&courseID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO sections (term_id, course_id, code, capacity) VALUES ($1, $2, '01', 100) RETURNING id`,
		termID, courseID).Scan(&sectionID); err != nil {
		t.Fatal(err)
	}
	return sectionID, courseID
}

func meeting(t *testing.T, pool *pgxpool.Pool, sectionID int64, day, from, to int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO section_meetings (section_id, day, start_min, end_min) VALUES ($1, $2, $3, $4)`,
		sectionID, day, from, to); err != nil {
		t.Fatal(err)
	}
}

func violationCodes(err error) map[string]bool {
	var rv *RuleViolationsError
	out := map[string]bool{}
	if errors.As(err, &rv) {
		for _, v := range rv.Report.Violations {
			out[v.Code] = true
		}
	}
	return out
}

// T-2: ihlaller tek transaction'da değerlendirilir, HEPSİ birden döner ve hiçbir şey yazılmaz.
func TestEnrollRuleViolationsAllAtOnce(t *testing.T) {
	pool := newTestPool(t)
	s1, ids := fixture(t, pool, 1, 100) // C1, 5 AKTS
	ctx := context.Background()
	student := ids[0]

	meeting(t, pool, s1, 1, 600, 720) // Pzt 10:00-12:00
	if _, err := enrollOnce(ctx, pool, student, s1); err != nil {
		t.Fatal(err)
	}

	// S2: ön koşulu sağlanmayan + S1 ile çakışan ders.
	s2, c2 := addSection(t, pool, "C2", 6)
	meeting(t, pool, s2, 1, 540, 660) // Pzt 09:00-11:00
	_, prereqCourse := addSection(t, pool, "P1", 6)
	expr := fmt.Sprintf(`{"course_id": %d}`, prereqCourse)
	if _, err := pool.Exec(ctx, `INSERT INTO course_prerequisites (course_id, expr) VALUES ($1, $2)`, c2, expr); err != nil {
		t.Fatal(err)
	}

	_, err := enrollOnce(ctx, pool, student, s2)
	got := violationCodes(err)
	if !got["PREREQ_NOT_MET"] || !got["TIME_CONFLICT"] || len(got) != 2 {
		t.Fatalf("beklenen {PREREQ_NOT_MET, TIME_CONFLICT}, gelen %v (err=%v)", got, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM enrollments WHERE section_id=$1`, s2); n != 0 {
		t.Errorf("reddedilen kayıt için %d satır yazıldı", n)
	}
	if n := count(t, pool, `SELECT enrolled_count FROM sections WHERE id=$1`, s2); n != 0 {
		t.Errorf("reddedilen şubenin sayacı %d, beklenen 0", n)
	}

	// Ön koşul geçilirse yalnızca çakışma kalır.
	if _, err := pool.Exec(ctx,
		`INSERT INTO course_results (student_id, course_id, term_id, letter, grade_point, passed)
		 SELECT $1, $2, id, 'BA', 3.5, true FROM terms LIMIT 1`, student, prereqCourse); err != nil {
		t.Fatal(err)
	}
	_, err = enrollOnce(ctx, pool, student, s2)
	if got := violationCodes(err); !got["TIME_CONFLICT"] || len(got) != 1 {
		t.Errorf("yalnızca TIME_CONFLICT beklenirdi: %v", got)
	}

	// AKTS: GANO 3.5 → limit 40; 5 (S1) + 36 → 41 aşar.
	s3, _ := addSection(t, pool, "BIG", 36)
	_, err = enrollOnce(ctx, pool, student, s3)
	if got := violationCodes(err); !got["ECTS_LIMIT_EXCEEDED"] {
		t.Errorf("AKTS ihlali beklenirdi: %v", got)
	}
}

// Aynı öğrenci 10 şubeye (6 AKTS) eşzamanlı kaydolmaya çalışır; limit 30 AKTS → tam 5 başarılı.
// Öğrenci satır kilidi olmasaydı her istek "henüz hiçbir şeyi yok" görüp hepsi geçebilirdi.
func TestSameStudentConcurrentECTSLimit(t *testing.T) {
	pool := newTestPool(t)
	_, ids := fixture(t, pool, 1, 100)
	ctx := context.Background()

	var sections []int64
	for i := 0; i < 10; i++ {
		sid, _ := addSection(t, pool, fmt.Sprintf("X%d", i), 6)
		sections = append(sections, sid)
	}
	var ok, rejected atomic.Int32
	var wg sync.WaitGroup
	for _, sid := range sections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := enrollOnce(ctx, pool, ids[0], sid)
			switch {
			case err == nil:
				ok.Add(1)
			case violationCodes(err)["ECTS_LIMIT_EXCEEDED"]:
				rejected.Add(1)
			default:
				t.Errorf("beklenmeyen hata: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 5 || rejected.Load() != 5 {
		t.Errorf("başarılı=%d reddedilen=%d, beklenen 5/5", ok.Load(), rejected.Load())
	}
	if n := count(t, pool, `SELECT count(*) FROM enrollments WHERE student_id=$1 AND status='enrolled'`, ids[0]); n != 5 {
		t.Errorf("öğrencinin aktif kaydı %d, beklenen 5", n)
	}
}
