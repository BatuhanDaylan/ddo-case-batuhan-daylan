package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/batuhan-daylan/uninet/internal/auth"
)

const verifyPw = "Verify123!"

// M kısa bir JSON nesnesi yazımıdır.
type M = map[string]any

// world koşunun paylaştığı akademik yapıdır (F-1 kurar, sonrakiler kullanır).
type world struct {
	fac, dept, prog int64
	course          map[string]int64
	v2020, v2024    int64
	term, term0     int64
	instrA, instrB  user
	advisor         user
	sections        map[string]int64 // ders adı → şube kimliği
	gs              []student        // F-5 not girişi öğrencileri
	gradeResult     map[int64]int64  // öğrenci kimliği → sonuç kimliği (F-5)
	f7              student          // F-7 mezuniyet öğrencisi
}

type user struct {
	id    int64
	email string
	token string
}

type student struct {
	sid, uid int64
	email    string
	token    string
}

// w zaman uyumsuz erişim için e.w alanını döner.
func (e *env) world() *world {
	if e.w == nil {
		e.w = &world{course: map[string]int64{}, sections: map[string]int64{}}
	}
	return e.w
}

func (e *env) bootstrap() error {
	hash, err := auth.HashPassword(verifyPw)
	if err != nil {
		return err
	}
	e.pwHash = hash
	for _, a := range []struct {
		dst   *string
		email string
	}{
		{&e.reg, "ogrenciisleri@uninet.test"}, {&e.sec, "sekreter@uninet.test"}, {&e.instr, "hoca@uninet.test"},
		{&e.adv, "danisman@uninet.test"}, {&e.stu, "ogrenci@uninet.test"}, {&e.rep, "rektorluk@uninet.test"},
	} {
		tok, _, st := e.login(a.email, auth.DemoPassword)
		if st != 200 {
			return fmt.Errorf("%s: %w (HTTP %d)", a.email, errNoDemo, st)
		}
		*a.dst = tok
	}
	return nil
}

// newUser doğrudan veritabanına (verifyPw parolasıyla) kullanıcı ekler ve gerçek giriş akışından token alır.
func (e *env) newUser(role, label string) (user, error) {
	email := fmt.Sprintf("v%s-%s@verify.test", e.sfx, strings.ToLower(label)) // sunucu girişte e-postayı küçük harfe çevirir
	var id int64
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, full_name, role) VALUES ($1, $2, $3, $4) RETURNING id`,
		email, e.pwHash, "Doğrulama "+label, role).Scan(&id); err != nil {
		return user{}, err
	}
	tok, _, st := e.login(email, verifyPw)
	if st != 200 {
		return user{}, fmt.Errorf("%s girişi: HTTP %d", email, st)
	}
	return user{id: id, email: email, token: tok}, nil
}

// newStudent bir öğrenci kullanıcısı + öğrenci kaydı ekler.
func (e *env) newStudent(t *T, label string, programID int64, entryYear int, advisor *user) student {
	u, err := e.newUser("student", label)
	t.must(err == nil, "öğrenci kurulumu %s: %v", label, err)
	var adv any
	if advisor != nil {
		adv = advisor.id
	}
	var sid int64
	err = e.pool.QueryRow(context.Background(),
		`INSERT INTO students (user_id, student_no, program_id, entry_year, advisor_id) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		u.id, fmt.Sprintf("V%s-%s", e.sfx, label), programID, entryYear, adv).Scan(&sid)
	t.must(err == nil, "öğrenci kaydı %s: %v", label, err)
	return student{sid: sid, uid: u.id, email: u.email, token: u.token}
}

// newStudents birçok öğrenciyi hızlıca kurar (tek SQL ile) ve girişlerini eşzamanlı yapar. T-1 için.
func (e *env) newStudents(t *T, prefix string, n int, programID int64, entryYear int) []student {
	ctx := context.Background()
	_, err := e.pool.Exec(ctx,
		`INSERT INTO users (email, password_hash, full_name, role)
		 SELECT 'v' || $1 || '-' || $2 || g || '@verify.test', $3, 'Doğrulama ' || g, 'student' FROM generate_series(1, $4) g`,
		e.sfx, prefix, e.pwHash, n)
	t.must(err == nil, "toplu kullanıcı: %v", err)
	_, err = e.pool.Exec(ctx,
		`INSERT INTO students (user_id, student_no, program_id, entry_year)
		 SELECT u.id, 'V' || $1 || '-' || $2 || substring(u.email from '[0-9]+@'), $3, $4
		   FROM users u WHERE u.email LIKE 'v' || $1 || '-' || $2 || '%@verify.test'`,
		e.sfx, prefix, programID, entryYear)
	t.must(err == nil, "toplu öğrenci: %v", err)

	rows, err := e.pool.Query(ctx,
		`SELECT s.id, s.user_id, u.email FROM students s JOIN users u ON u.id = s.user_id
		  WHERE u.email LIKE 'v' || $1 || '-' || $2 || '%@verify.test' ORDER BY s.id`, e.sfx, prefix)
	t.must(err == nil, "öğrenci listesi: %v", err)
	var out []student
	for rows.Next() {
		var s student
		t.must(rows.Scan(&s.sid, &s.uid, &s.email) == nil, "tarama")
		out = append(out, s)
	}
	rows.Close()
	t.must(len(out) == n, "beklenen %d öğrenci, kurulan %d", n, len(out))

	// Girişler eşzamanlı (argon2 sunucuda sınırlı eşzamanlılıkla çalışır).
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var mu sync.Mutex
	failed := 0
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tok, _, st := e.login(out[i].email, verifyPw)
			mu.Lock()
			defer mu.Unlock()
			if st != 200 {
				failed++
				return
			}
			out[i].token = tok
		}()
	}
	wg.Wait()
	t.must(failed == 0, "%d öğrenci giriş yapamadı", failed)
	return out
}

// pass bir öğrencinin bir dersi geçtiğini (transkript satırı) doğrudan veritabanına yazar.
func (e *env) pass(t *T, studentID, courseID, termID int64, letter string, point float64) {
	_, err := e.pool.Exec(context.Background(),
		`INSERT INTO course_results (student_id, course_id, term_id, letter, grade_point, passed) VALUES ($1, $2, $3, $4, $5, $6)`,
		studentID, courseID, termID, letter, point, point >= 1)
	t.must(err == nil, "sonuç ekleme: %v", err)
}

// enroll öğrenci adına Idempotency-Key ile kayıt isteği gönderir.
func (e *env) enroll(token string, sectionID int64, key string) R {
	return e.call("POST", fmt.Sprintf("/sections/%d/enrollments", sectionID), token, nil, "Idempotency-Key", key)
}

func p(format string, a ...any) string { return fmt.Sprintf(format, a...) }
