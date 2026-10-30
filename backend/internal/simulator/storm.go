package simulator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/auth"
)

// StormTarget kayıt anı yükünün hedefidir: öğrenciler ve kontenjanı 40 olan şubeler.
type StormTarget struct {
	Students []StormStudent
	Sections []int64
}

type StormStudent struct {
	StudentID int64  `json:"student_id"`
	UserID    int64  `json:"user_id"`
	Token     string `json:"token"`
}

// LoadTarget ilk n simülasyon öğrencisi için token üretir ve kayıt anı şubelerini bulur.
// Token'lar doğrudan JWT sırrıyla imzalanır (bu bir test aracıdır; 5.000 argon2 girişi yapmak yerine).
func LoadTarget(ctx context.Context, pool *pgxpool.Pool, jwtSecret string, n int) (StormTarget, error) {
	var t StormTarget
	iss := auth.NewIssuer(jwtSecret, 3*time.Hour)

	rows, err := pool.Query(ctx,
		`SELECT id, user_id FROM students WHERE student_no LIKE 'SIM%' ORDER BY id LIMIT $1`, n)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var s StormStudent
		if err := rows.Scan(&s.StudentID, &s.UserID); err != nil {
			rows.Close()
			return t, err
		}
		if s.Token, err = iss.Access(s.UserID, auth.RoleStudent); err != nil {
			rows.Close()
			return t, err
		}
		t.Students = append(t.Students, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	srows, err := pool.Query(ctx,
		`SELECT s.id FROM sections s JOIN courses c ON c.id = s.course_id WHERE c.code LIKE 'SIM-STORM-%' ORDER BY s.id`)
	if err != nil {
		return t, err
	}
	defer srows.Close()
	for srows.Next() {
		var id int64
		if err := srows.Scan(&id); err != nil {
			return t, err
		}
		t.Sections = append(t.Sections, id)
	}
	if len(t.Students) == 0 || len(t.Sections) == 0 {
		return t, fmt.Errorf("simülasyon verisi yok: önce 'simulator seed' çalıştırın")
	}
	return t, srows.Err()
}

// WriteTokens k6 gibi dış araçlar için hedefi JSON dosyasına yazar.
func WriteTokens(path string, t StormTarget) error {
	b, err := json.Marshal(map[string]any{"students": t.Students, "sections": t.Sections})
	if err != nil {
		return err
	}
	// 0644: k6 farklı bir kullanıcıyla çalışır ve dosyayı okuyabilmelidir. İçinde yalnızca kısa ömürlü
	// test token'ları vardır (üretim kullanıcılarına ait değil).
	return os.WriteFile(path, b, 0o644)
}

// StormResult kayıt anı yükünün özetidir.
type StormResult struct {
	Requests    int
	Duration    time.Duration
	ByStatus    map[int]int
	ByOutcome   map[string]int // enrolled | waitlisted | diğer hata kodları
	P50, P95    time.Duration
	P99, Max    time.Duration
	NetworkErrs int
}

func (r StormResult) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "istek: %d  süre: %v  verim: %.0f istek/sn\n", r.Requests, r.Duration.Round(time.Millisecond), float64(r.Requests)/r.Duration.Seconds())
	fmt.Fprintf(&sb, "gecikme: p50=%v p95=%v p99=%v max=%v\n", r.P50.Round(time.Millisecond), r.P95.Round(time.Millisecond), r.P99.Round(time.Millisecond), r.Max.Round(time.Millisecond))
	codes := make([]int, 0, len(r.ByStatus))
	for c := range r.ByStatus {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	for _, c := range codes {
		fmt.Fprintf(&sb, "  HTTP %d: %d\n", c, r.ByStatus[c])
	}
	outs := make([]string, 0, len(r.ByOutcome))
	for o := range r.ByOutcome {
		outs = append(outs, o)
	}
	sort.Strings(outs)
	for _, o := range outs {
		fmt.Fprintf(&sb, "  sonuç %s: %d\n", o, r.ByOutcome[o])
	}
	fmt.Fprintf(&sb, "  ağ hatası: %d\n", r.NetworkErrs)
	return sb.String()
}

// Storm, hedefteki her öğrencinin perStudent farklı şubeye eşzamanlı kayıt isteği göndermesini taklit eder.
// Her istek benzersiz Idempotency-Key taşır. concurrency eşzamanlı istek sayısını sınırlar.
func Storm(ctx context.Context, baseURL string, t StormTarget, perStudent, concurrency int, seed uint64) StormResult {
	type job struct {
		token   string
		student int64
		section int64
	}
	rng := rand.New(rand.NewPCG(seed, 0x57041))
	var jobs []job
	for _, s := range t.Students {
		perm := rng.Perm(len(t.Sections))
		for k := 0; k < perStudent && k < len(perm); k++ {
			jobs = append(jobs, job{s.Token, s.StudentID, t.Sections[perm[k]]})
		}
	}
	rng.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] }) // tam karışık varış sırası

	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency, MaxConnsPerHost: concurrency},
	}
	var (
		mu        sync.Mutex
		res       = StormResult{ByStatus: map[int]int{}, ByOutcome: map[string]int{}, Requests: len(jobs)}
		latencies = make([]time.Duration, 0, len(jobs))
		wg        sync.WaitGroup
		ch        = make(chan job)
	)
	start := time.Now()
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/sections/%d/enrollments", baseURL, j.section), nil)
				req.Header.Set("Authorization", "Bearer "+j.token)
				req.Header.Set("Idempotency-Key", fmt.Sprintf("storm-%d-%d", j.student, j.section))
				t0 := time.Now()
				resp, err := client.Do(req)
				d := time.Since(t0)
				mu.Lock()
				latencies = append(latencies, d)
				if err != nil {
					res.NetworkErrs++
					mu.Unlock()
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				res.ByStatus[resp.StatusCode]++
				var parsed struct {
					Status string `json:"status"`
					Error  struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				_ = json.Unmarshal(body, &parsed)
				switch {
				case parsed.Status != "":
					res.ByOutcome[parsed.Status]++
				case parsed.Error.Code != "":
					res.ByOutcome[parsed.Error.Code]++
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()
	res.Duration = time.Since(start)

	slices.Sort(latencies)
	q := func(p float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[min(len(latencies)-1, int(p*float64(len(latencies))))]
	}
	res.P50, res.P95, res.P99 = q(.50), q(.95), q(.99)
	if len(latencies) > 0 {
		res.Max = latencies[len(latencies)-1]
	}
	return res
}
