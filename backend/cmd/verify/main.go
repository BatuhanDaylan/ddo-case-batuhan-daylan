// verify: ayakta bir UniNet sistemini HTTP üzerinden uçtan uca doğrular (şartname Bölüm 10).
//
// Her kapsam maddesi (F-1…F-8) ve her zorlu görev (T-1…T-7) ile sağlık, kimlik doğrulama ve yetki ihlali
// senaryoları denenir; sonunda kimlik başına PASS/FAIL tablosu basılır ve herhangi biri başarısızsa çıkış kodu 1 olur.
// Her koşu kendi benzersiz verisini kurar (ek: zaman damgası), bu yüzden `make reset-demo` olmadan da tekrarlanabilir.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/db"
)

// abort bir kontrolün ilk başarısız doğrulamada durdurulması için kullanılır.
type abort struct{}

// T bir kimliğin (F-1, T-3, ...) doğrulama bağlamıdır.
type T struct {
	id, name string
	errs     []string
	notes    []string
}

func (t *T) fail(format string, a ...any) { t.errs = append(t.errs, fmt.Sprintf(format, a...)) }

// must koşul sağlanmazsa kontrolü durdurur.
func (t *T) must(ok bool, format string, a ...any) {
	if !ok {
		t.fail(format, a...)
		panic(abort{})
	}
}

// soft koşul sağlanmazsa hatayı kaydeder ama devam eder.
func (t *T) soft(ok bool, format string, a ...any) {
	if !ok {
		t.fail(format, a...)
	}
}

func (t *T) note(format string, a ...any) { t.notes = append(t.notes, fmt.Sprintf(format, a...)) }

// R bir HTTP yanıtıdır.
type R struct {
	Status int
	Raw    []byte
	M      map[string]any
	Header http.Header
}

func (r R) val(path string) any {
	var cur any = r.M
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (r R) str(path string) string   { s, _ := r.val(path).(string); return s }
func (r R) num(path string) float64  { f, _ := r.val(path).(float64); return f }
func (r R) id(path string) int64     { return int64(r.num(path)) }
func (r R) arr(path string) []any    { a, _ := r.val(path).([]any); return a }
func (r R) boolean(path string) bool { b, _ := r.val(path).(bool); return b }

// codes hata yanıtındaki violations/conflicts listesinden ilgili alanları toplar.
func (r R) listField(path, field string) []string {
	var out []string
	for _, x := range r.arr(path) {
		if m, ok := x.(map[string]any); ok {
			if s, ok := m[field].(string); ok {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

type env struct {
	base string
	cl   *http.Client
	pool *pgxpool.Pool
	sfx  string // bu koşuya özgü ek
	// demo hesap token'ları
	reg, sec, instr, adv, stu, rep string
	pwHash                         string
	w                              *world
}

func (e *env) call(method, path, token string, body any, hdr ...string) R {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := e.cl.Do(req)
	if err != nil {
		return R{Status: 0, Raw: []byte(err.Error())}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := R{Status: resp.StatusCode, Raw: raw, Header: resp.Header}
	_ = json.Unmarshal(raw, &r.M)
	return r
}

func (e *env) login(email, password string) (access, refresh string, status int) {
	r := e.call("POST", "/auth/login", "", map[string]string{"email": email, "password": password})
	return r.str("access_token"), r.str("refresh_token"), r.Status
}

type check struct {
	id, name string
	fn       func(t *T, e *env)
}

func main() {
	base := os.Getenv("API_URL")
	if base == "" {
		base = "http://backend:8080"
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL zorunlu (test verisi kurmak için)")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, dsn, 10)
	if err != nil {
		fmt.Fprintln(os.Stderr, "veritabanı:", err)
		os.Exit(2)
	}
	defer pool.Close()

	e := &env{base: base, cl: &http.Client{Timeout: 60 * time.Second}, pool: pool, sfx: fmt.Sprintf("%x", time.Now().UnixNano()%0xffffffff)}
	if err := e.bootstrap(); err != nil {
		fmt.Fprintln(os.Stderr, "hazırlık başarısız:", err)
		os.Exit(2)
	}

	start := time.Now()
	failed := 0
	fmt.Printf("\nUniNet doğrulaması  (%s, koşu %s)\n", base, e.sfx)
	fmt.Println(strings.Repeat("─", 100))
	fmt.Printf("%-8s %-6s %-62s %s\n", "KİMLİK", "SONUÇ", "AÇIKLAMA", "SÜRE")
	fmt.Println(strings.Repeat("─", 100))
	var details []string
	for _, c := range allChecks() {
		t := &T{id: c.id, name: c.name}
		t0 := time.Now()
		func() {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(abort); !ok {
						t.fail("beklenmeyen hata: %v", r)
					}
				}
			}()
			c.fn(t, e)
		}()
		status := "PASS"
		if len(t.errs) > 0 {
			status = "FAIL"
			failed++
			for _, m := range t.errs {
				details = append(details, fmt.Sprintf("  [%s] %s", c.id, m))
			}
		}
		fmt.Printf("%-8s %-6s %-62s %s\n", c.id, status, truncate(c.name, 62), time.Since(t0).Round(time.Millisecond))
		for _, n := range t.notes {
			fmt.Printf("%-8s %-6s   · %s\n", "", "", n)
		}
	}
	fmt.Println(strings.Repeat("─", 100))
	if len(details) > 0 {
		fmt.Println("Başarısız doğrulamalar:")
		for _, d := range details {
			fmt.Println(d)
		}
		fmt.Println(strings.Repeat("─", 100))
	}
	total := len(allChecks())
	fmt.Printf("Toplam %d kimlik: %d PASS, %d FAIL  (%v)\n", total, total-failed, failed, time.Since(start).Round(time.Second))
	if failed > 0 {
		os.Exit(1)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var errNoDemo = errors.New("demo hesaplar giriş yapamadı: sistem SEED_DEMO=true ile başlatılmış olmalı")
