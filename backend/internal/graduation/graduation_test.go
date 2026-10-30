package graduation

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func passed(codes ...string) map[string]int {
	m := map[string]int{}
	for _, c := range codes {
		m[c] = 3
	}
	return m
}

func manyPools(n int) []Pool {
	var ps []Pool
	for i := 0; i < n; i++ {
		ps = append(ps, Pool{Name: fmt.Sprint("P", i), Courses: []string{"X"}, MinCourses: 1})
	}
	return ps
}

func TestValidateRules(t *testing.T) {
	ok := Rules{MinGANO: 2, MinTotalECTS: 240, Pools: []Pool{{Name: "A", Courses: []string{"X"}, MinCourses: 1}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("geçerli kural reddedildi: %v", err)
	}
	bad := map[string]Rules{
		"gano":        {MinGANO: 5},
		"ects":        {MinTotalECTS: -1},
		"tekrar":      {Required: []string{"A", "A"}},
		"boş kod":     {Required: []string{" "}},
		"ad boş":      {Pools: []Pool{{Name: "", Courses: []string{"X"}, MinCourses: 1}}},
		"ad tekrar":   {Pools: []Pool{{Name: "A", Courses: []string{"X"}, MinCourses: 1}, {Name: "A", Courses: []string{"Y"}, MinCourses: 1}}},
		"ders yok":    {Pools: []Pool{{Name: "A", MinCourses: 1}}},
		"ders tekrar": {Pools: []Pool{{Name: "A", Courses: []string{"X", "X"}, MinCourses: 1}}},
		"eşik sıfır":  {Pools: []Pool{{Name: "A", Courses: []string{"X"}}}},
		"eşik büyük":  {Pools: []Pool{{Name: "A", Courses: []string{"X"}, MinCourses: 999}}},
		"staj boş":    {Internship: &Internship{}},
		"çok havuz":   {Pools: manyPools(9)},
	}
	for name, r := range bad {
		if r.Validate() == nil {
			t.Errorf("%s: geçersiz kural kabul edildi", name)
		}
	}
	r := Rules{Required: []string{"B", "A"}, Pools: []Pool{{Name: "P", Courses: []string{"C", "A"}, MinCourses: 1}},
		Internship: &Internship{Course: "S"}}
	if got := r.AllCodes(); !slices.Equal(got, []string{"A", "B", "C", "S"}) {
		t.Errorf("AllCodes = %v", got)
	}
}

func TestGraduateHappyPath(t *testing.T) {
	r := Rules{MinGANO: 2, MinTotalECTS: 30, Pools: []Pool{{Name: "Seçmeli", Courses: []string{"E1", "E2", "E3", "E4"}, MinCourses: 3, MinECTS: 9}},
		Internship: &Internship{Course: "STJ"}}
	rep, err := Evaluate(Input{Rules: r, Required: []string{"R1", "R2"}, Passed: passed("R1", "R2", "E1", "E2", "E3", "STJ"), TotalECTS: 36, GANO: 2.5})
	if err != nil || !rep.CanGraduate || len(rep.Missing) != 0 {
		t.Fatalf("mezun olabilmeli: %+v err=%v", rep, err)
	}
}

func TestMissingItemsAreAllListed(t *testing.T) {
	r := Rules{MinGANO: 2, MinTotalECTS: 100, Pools: []Pool{{Name: "Seçmeli", Courses: []string{"E1", "E2", "E3"}, MinCourses: 2, MinECTS: 6}},
		Internship: &Internship{Course: "STJ"}}
	rep, _ := Evaluate(Input{Rules: r, Required: []string{"R1", "R2"}, Passed: passed("R1", "E1"), TotalECTS: 6, GANO: 1.5})
	if rep.CanGraduate {
		t.Fatal("eksikler varken mezun görünüyor")
	}
	if !slices.Equal(rep.RequiredMissing, []string{"R2"}) {
		t.Errorf("eksik zorunlu = %v", rep.RequiredMissing)
	}
	p := rep.Pools[0]
	if p.MissingCourses != 1 || p.MissingECTS != 3 || !slices.Equal(p.Remaining, []string{"E2", "E3"}) {
		t.Errorf("havuz raporu: %+v", p)
	}
	// Tüm nedenler tek raporda: zorunlu, havuz, staj, GANO, AKTS.
	if len(rep.Missing) != 5 {
		t.Errorf("özet satırı %d, beklenen 5: %v", len(rep.Missing), rep.Missing)
	}
	if rep.GANOOK || rep.ECTSOK || rep.Internship.OK || rep.RequiredOK {
		t.Errorf("bayraklar yanlış: %+v", rep)
	}
}

// Klasik açgözlü tuzağı: C2 hem A hem B havuzunda geçerli. B yalnızca C2/C3'ten, A yalnızca C1/C2'den
// besleniyor. Öğrenci C2 ve C3'ü geçti. Doğru atama: C2→A, C3→B. "C2'yi önce B'ye koy" yanlıştır.
func TestOptimalAssignmentBeatsGreedy(t *testing.T) {
	r := Rules{Pools: []Pool{
		{Name: "B", Courses: []string{"C2", "C3"}, MinCourses: 1},
		{Name: "A", Courses: []string{"C1", "C2"}, MinCourses: 1},
	}}
	rep, err := Evaluate(Input{Rules: r, Passed: passed("C2", "C3"), GANO: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pools[0].OK || !rep.Pools[1].OK {
		t.Fatalf("iki havuz da doyurulabilirdi: %+v", rep.Pools)
	}
	if !slices.Equal(rep.Pools[0].Assigned, []string{"C3"}) || !slices.Equal(rep.Pools[1].Assigned, []string{"C2"}) {
		t.Errorf("atama B=%v A=%v, beklenen B=[C3] A=[C2]", rep.Pools[0].Assigned, rep.Pools[1].Assigned)
	}
}

func TestSharedCourseCountsOnce(t *testing.T) {
	// Tek ders iki havuzun da şartını sağlayamaz: yalnızca biri doyar, diğeri 1 ders eksik kalır.
	r := Rules{Pools: []Pool{
		{Name: "A", Courses: []string{"X"}, MinCourses: 1},
		{Name: "B", Courses: []string{"X"}, MinCourses: 1},
	}}
	rep, _ := Evaluate(Input{Rules: r, Passed: passed("X")})
	ok := 0
	for _, p := range rep.Pools {
		if p.OK {
			ok++
		}
	}
	if ok != 1 || rep.CanGraduate {
		t.Errorf("tek ders iki havuza sayıldı: %+v", rep.Pools)
	}
}

func TestRequiredAndInternshipAreNotCountedInPools(t *testing.T) {
	r := Rules{Pools: []Pool{{Name: "A", Courses: []string{"R1", "STJ", "E1"}, MinCourses: 2}}, Internship: &Internship{Course: "STJ"}}
	rep, _ := Evaluate(Input{Rules: r, Required: []string{"R1"}, Passed: passed("R1", "STJ", "E1")})
	// R1 zorunlu, STJ staj olarak tüketildi; havuz için yalnızca E1 kalır → 1 ders eksik.
	if rep.Pools[0].OK || rep.Pools[0].MissingCourses != 1 || !slices.Equal(rep.Pools[0].Assigned, []string{"E1"}) {
		t.Errorf("zorunlu/staj havuza sayıldı: %+v", rep.Pools[0])
	}
}

func TestECTSOnlyPoolAndPartialReport(t *testing.T) {
	r := Rules{Pools: []Pool{{Name: "A", Courses: []string{"X", "Y"}, MinECTS: 9}}}
	rep, _ := Evaluate(Input{Rules: r, Passed: map[string]int{"X": 3, "Y": 3}})
	if rep.Pools[0].OK || rep.Pools[0].MissingECTS != 3 || rep.Pools[0].AssignedECTS != 6 {
		t.Errorf("kısmi ilerleme: %+v", rep.Pools[0])
	}
}

func TestNoPoolsAndDeterminism(t *testing.T) {
	rep, err := Evaluate(Input{Rules: Rules{MinGANO: 2}, GANO: 2})
	if err != nil || !rep.CanGraduate {
		t.Errorf("havuzsuz kural: %+v err=%v", rep, err)
	}
	r := Rules{Pools: []Pool{
		{Name: "A", Courses: []string{"a", "b", "c", "d"}, MinCourses: 2},
		{Name: "B", Courses: []string{"a", "b", "c", "d"}, MinCourses: 2},
	}}
	first, _ := Evaluate(Input{Rules: r, Passed: passed("a", "b", "c", "d")})
	for i := 0; i < 20; i++ {
		again, _ := Evaluate(Input{Rules: r, Passed: passed("a", "b", "c", "d")})
		if fmt.Sprint(again.Pools) != fmt.Sprint(first.Pools) {
			t.Fatal("sonuç deterministik değil")
		}
	}
}

func TestTooComplexIsReported(t *testing.T) {
	old := maxStates
	maxStates = 3
	defer func() { maxStates = old }()
	r := Rules{Pools: []Pool{
		{Name: "A", Courses: []string{"a", "b", "c", "d"}, MinCourses: 3},
		{Name: "B", Courses: []string{"a", "b", "c", "d"}, MinCourses: 3},
	}}
	if _, err := Evaluate(Input{Rules: r, Passed: passed("a", "b", "c", "d")}); err != ErrTooComplex {
		t.Errorf("hata %v, beklenen ErrTooComplex", err)
	}
}

// Özellik: çözücünün bulduğu eksik, KABA KUVVETLE bulunan en küçük eksikle aynıdır (gerçekten en iyi).
func TestSolverMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	for iter := 0; iter < 400; iter++ {
		np := 1 + rng.IntN(3)
		nc := 1 + rng.IntN(7)
		codes := make([]string, nc)
		ectsOf := map[string]int{}
		for i := range codes {
			codes[i] = fmt.Sprint("C", i)
			ectsOf[codes[i]] = 2 + rng.IntN(4)
		}
		var pools []Pool
		for p := 0; p < np; p++ {
			var members []string
			for _, c := range codes {
				if rng.IntN(2) == 0 {
					members = append(members, c)
				}
			}
			if len(members) == 0 {
				members = []string{codes[rng.IntN(nc)]}
			}
			pool := Pool{Name: fmt.Sprint("P", p), Courses: members, MinCourses: rng.IntN(3), MinECTS: rng.IntN(8)}
			if pool.MinCourses == 0 && pool.MinECTS == 0 {
				pool.MinCourses = 1
			}
			pools = append(pools, pool)
		}
		rep, err := Evaluate(Input{Rules: Rules{Pools: pools}, Passed: ectsOf})
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		seen := map[string]bool{}
		for pi, pr := range rep.Pools {
			got += pr.MissingECTS*1000 + pr.MissingCourses
			for _, c := range pr.Assigned {
				if seen[c] {
					t.Fatalf("ders %s birden çok havuza sayıldı", c)
				}
				seen[c] = true
				if !slices.Contains(pools[pi].Courses, c) {
					t.Fatalf("ders %s havuz %s üyesi değil", c, pools[pi].Name)
				}
			}
		}
		if want := bruteForce(pools, codes, ectsOf); got != want {
			t.Fatalf("iter %d: çözücü eksiği %d, kaba kuvvet %d (havuzlar=%+v)", iter, got, want, pools)
		}
	}
}

// bruteForce her dersi (havuz | hiçbiri) olarak atayan tüm kombinasyonları dener.
func bruteForce(pools []Pool, codes []string, ects map[string]int) int {
	best := int(^uint(0) >> 1)
	assign := make([]int, len(codes)) // -1: hiçbiri
	var rec func(i int)
	rec = func(i int) {
		if i == len(codes) {
			cnt := make([]int, len(pools))
			ec := make([]int, len(pools))
			for ci, pi := range assign {
				if pi >= 0 {
					cnt[pi]++
					ec[pi] += ects[codes[ci]]
				}
			}
			def := 0
			for pi, p := range pools {
				def += max(0, p.MinECTS-ec[pi])*1000 + max(0, p.MinCourses-cnt[pi])
			}
			best = min(best, def)
			return
		}
		assign[i] = -1
		rec(i + 1)
		for pi, p := range pools {
			if slices.Contains(p.Courses, codes[i]) {
				assign[i] = pi
				rec(i + 1)
			}
		}
	}
	rec(0)
	return best
}
