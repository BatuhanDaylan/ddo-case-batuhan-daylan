package simulator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/testdb"
)

func digest(d Dataset) string {
	h := sha256.New()
	for _, t := range d.Tables {
		fmt.Fprintf(h, "%s|%v|%d\n", t.Name, t.Cols, len(t.Rows))
		for _, r := range t.Rows {
			fmt.Fprintln(h, r...)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Şartname: "Aynı tohumla aynı veri üretilmeli."
func TestSameSeedSameDataDifferentSeedDifferentData(t *testing.T) {
	a := Generate(42, Full, "hash")
	b := Generate(42, Full, "hash")
	if digest(a) != digest(b) {
		t.Fatal("aynı tohum farklı veri üretti")
	}
	if digest(a) == digest(Generate(43, Full, "hash")) {
		t.Error("farklı tohum aynı veriyi üretti")
	}
}

func TestFullScaleMatchesSpec(t *testing.T) {
	d := Generate(42, Full, "hash")
	want := map[string]int{"students": 12000, "courses": 600, "sections": 1500, "terms": 9}
	for table, n := range want {
		if got := len(d.Table(table).Rows); got != n {
			t.Errorf("%s: %d satır, beklenen %d", table, got, n)
		}
	}
	// 4 yıllık geçmiş not verisi: yüz binlerce satır.
	if n := len(d.Table("course_results").Rows); n < 100_000 {
		t.Errorf("not geçmişi %d satır; 4 yıllık veri için yetersiz", n)
	}
	// Kayıt anı şubeleri: tam 30 şube, kontenjan 40.
	storm := 0
	for _, r := range d.Table("sections").Rows {
		if r[5].(int) == 40 {
			storm++
		}
	}
	if storm < 30 {
		t.Errorf("kontenjanı 40 olan şube %d, beklenen en az 30", storm)
	}
}

func TestReferentialIntegrityAndUniqueness(t *testing.T) {
	d := Generate(7, Full, "hash")
	ids := func(table string) map[int64]bool {
		m := map[int64]bool{}
		for _, r := range d.Table(table).Rows {
			id := r[0].(int64)
			if m[id] {
				t.Fatalf("%s: tekrarlayan kimlik %d", table, id)
			}
			m[id] = true
		}
		return m
	}
	users, courses, terms, students, sections := ids("users"), ids("courses"), ids("terms"), ids("students"), ids("sections")
	_ = sections
	for _, r := range d.Table("sections").Rows {
		if !terms[r[1].(int64)] || !courses[r[2].(int64)] || !users[r[4].(int64)] {
			t.Fatalf("şube yabancı anahtarı kırık: %v", r)
		}
	}
	emails := map[string]bool{}
	for _, r := range d.Table("users").Rows {
		if emails[r[1].(string)] {
			t.Fatalf("e-posta tekrar ediyor: %v", r[1])
		}
		emails[r[1].(string)] = true
	}
	seen := map[[3]int64]bool{}
	for _, r := range d.Table("course_results").Rows {
		k := [3]int64{r[1].(int64), r[2].(int64), r[3].(int64)}
		if !students[k[0]] || !courses[k[1]] || !terms[k[2]] {
			t.Fatalf("sonuç yabancı anahtarı kırık: %v", r)
		}
		if seen[k] {
			t.Fatalf("(öğrenci, ders, dönem) tekrar ediyor: %v", k)
		}
		seen[k] = true
	}
}

// Gerçek veritabanı: yükleme çalışır, idempotenttir, kota denetimi temizdir ve hedef doğru bulunur.
func TestLoadIsIdempotentAndTargetsFound(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	small := Scale{Students: 100, Courses: 60, Sections: 150, Storm: 6}
	d := Generate(1, small, "x")

	if err := Load(ctx, pool, d); err != nil {
		t.Fatal(err)
	}
	if err := Load(ctx, pool, d); err != ErrAlreadySeeded {
		t.Errorf("ikinci yükleme: %v, beklenen ErrAlreadySeeded", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM students`).Scan(&n); err != nil || n != 100 {
		t.Errorf("öğrenci sayısı %d err=%v", n, err)
	}
	// Kimlik sıraları ayarlanmış olmalı: yeni normal ekleme çakışmamalı.
	if _, err := pool.Exec(ctx, `INSERT INTO faculties (code, name) VALUES ('YENI', 'Yeni')`); err != nil {
		t.Errorf("yükleme sonrası normal ekleme çakıştı: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM gano_jobs`); n != 100 {
		t.Errorf("GANO kuyruğu %d, beklenen 100", n)
	}

	target, err := LoadTarget(ctx, pool, "jwt-secret-jwt-secret-jwt-secret-1234", 50)
	if err != nil || len(target.Students) != 50 || len(target.Sections) != 6 {
		t.Fatalf("hedef: öğrenci=%d şube=%d err=%v", len(target.Students), len(target.Sections), err)
	}
	if v, err := CheckQuota(ctx, pool); err != nil || len(v) != 0 {
		t.Errorf("temiz veride ihlal: %v err=%v", v, err)
	}

	// Bozuk durumlar yakalanmalı: kontenjanı aşan sayaç.
	if _, err := pool.Exec(ctx, `ALTER TABLE sections DROP CONSTRAINT sections_quota_ok`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sections SET enrolled_count = capacity + 1 WHERE id = (SELECT min(id) FROM sections)`); err != nil {
		t.Fatal(err)
	}
	v, err := CheckQuota(ctx, pool)
	if err != nil || len(v) == 0 {
		t.Errorf("kota aşımı yakalanmadı: %v err=%v", v, err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// Fırtına istemcisi: sahte sunucuyla sayım, durum kodu ve sonuç toplamasını doğrular.
func TestStormAggregatesResults(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" || r.Header.Get("Authorization") == "" {
			t.Error("istek başlıkları eksik")
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case n%3 == 0:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "rule_violation"}})
		case n%3 == 1:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"enrolled"}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"waitlisted"}`))
		}
	}))
	defer srv.Close()

	target := StormTarget{Sections: []int64{1, 2, 3, 4}}
	for i := 0; i < 30; i++ {
		target.Students = append(target.Students, StormStudent{StudentID: int64(i + 1), Token: "t"})
	}
	res := Storm(context.Background(), srv.URL, target, 3, 8, 1)
	if res.Requests != 90 || calls.Load() != 90 {
		t.Fatalf("istek %d / sunucuya ulaşan %d, beklenen 90", res.Requests, calls.Load())
	}
	if res.ByStatus[201]+res.ByStatus[422] != 90 || res.NetworkErrs != 0 {
		t.Errorf("durum dağılımı: %v ağ hatası %d", res.ByStatus, res.NetworkErrs)
	}
	if res.ByOutcome["enrolled"] == 0 || res.ByOutcome["waitlisted"] == 0 || res.ByOutcome["rule_violation"] == 0 {
		t.Errorf("sonuçlar: %v", res.ByOutcome)
	}
	if res.P50 <= 0 || res.P99 < res.P50 || res.Max < res.P99 {
		t.Errorf("gecikme yüzdelikleri tutarsız: %v %v %v", res.P50, res.P99, res.Max)
	}
	if s := res.String(); s == "" {
		t.Error("özet boş")
	}
}
