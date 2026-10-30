package rules

import (
	"encoding/json"
	"math/rand/v2"
	"slices"
	"testing"
)

func leaf(id int64) Node { return Node{CourseID: id} }
func and(a ...Node) Node { return Node{Op: OpAnd, Args: a} }
func or(a ...Node) Node  { return Node{Op: OpOr, Args: a} }

func set(ids ...int64) map[int64]bool {
	m := map[int64]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func TestEval(t *testing.T) {
	// A ve (B veya C)
	expr := and(leaf(1), or(leaf(2), leaf(3)))
	cases := []struct {
		passed map[int64]bool
		want   bool
	}{
		{set(), false},
		{set(1), false},
		{set(2, 3), false},
		{set(1, 2), true},
		{set(1, 3), true},
		{set(1, 2, 3), true},
	}
	for _, c := range cases {
		if got := expr.Eval(c.passed); got != c.want {
			t.Errorf("Eval(%v) = %v, beklenen %v", c.passed, got, c.want)
		}
	}
}

func TestDescribeAndCourses(t *testing.T) {
	codes := map[int64]string{1: "BM101", 2: "MAT101", 3: "FIZ101"}
	expr := and(leaf(1), or(leaf(2), leaf(3)))
	if got, want := expr.Describe(codes), "BM101 ve (MAT101 veya FIZ101)"; got != want {
		t.Errorf("Describe = %q, beklenen %q", got, want)
	}
	if got := leaf(9).Describe(codes); got != "#9" {
		t.Errorf("bilinmeyen kod: %q", got)
	}
	if got := or(and(leaf(1), leaf(2)), leaf(3)).Describe(codes); got != "(BM101 ve MAT101) veya FIZ101" {
		t.Errorf("iç içe: %q", got)
	}
	if !slices.Equal(expr.Courses(), []int64{1, 2, 3}) {
		t.Errorf("Courses = %v", expr.Courses())
	}
}

func TestNodeValidate(t *testing.T) {
	bad := []Node{
		{},                                   // boş
		{Op: "xor", Args: []Node{leaf(1)}},   // bilinmeyen işlem
		{Op: OpAnd},                          // alt ifadesiz
		{CourseID: -1},                       // geçersiz kimlik
		{CourseID: 1, Args: []Node{leaf(2)}}, // yaprakta alt ifade
		{Op: OpOr, CourseID: 4, Args: []Node{leaf(2)}},
		and(leaf(1), Node{}), // alt düğüm geçersiz
	}
	for i, n := range bad {
		if n.Validate() == nil {
			t.Errorf("geçersiz ifade #%d kabul edildi: %+v", i, n)
		}
	}
	if err := and(leaf(1), or(leaf(2), leaf(3))).Validate(); err != nil {
		t.Errorf("geçerli ifade reddedildi: %v", err)
	}

	// Derinlik sınırı
	deep := leaf(1)
	for i := 0; i < maxDepth+1; i++ {
		deep = and(deep)
	}
	if deep.Validate() == nil {
		t.Error("çok derin ifade kabul edildi")
	}
	// Düğüm sayısı sınırı
	wide := Node{Op: OpOr}
	for i := 0; i < maxNodes+1; i++ {
		wide.Args = append(wide.Args, leaf(int64(i+1)))
	}
	if wide.Validate() == nil {
		t.Error("çok geniş ifade kabul edildi")
	}
}

func TestDetectCycle(t *testing.T) {
	// 1 → 2 → 3 → 1 döngüsü
	g := map[int64]Node{1: leaf(2), 2: leaf(3), 3: leaf(1)}
	c := DetectCycle(g)
	if len(c) != 4 || c[0] != c[len(c)-1] {
		t.Fatalf("döngü bulunamadı/yanlış: %v", c)
	}
	// Kendine bağımlılık
	if DetectCycle(map[int64]Node{5: and(leaf(6), leaf(5))}) == nil {
		t.Error("öz-döngü bulunamadı")
	}
	// Elmas şekli döngü DEĞİLDİR: 4 → (2,3), 2 → 1, 3 → 1
	diamond := map[int64]Node{4: and(leaf(2), leaf(3)), 2: leaf(1), 3: leaf(1)}
	if c := DetectCycle(diamond); c != nil {
		t.Errorf("elmas yanlış döngü sayıldı: %v", c)
	}
	if DetectCycle(nil) != nil {
		t.Error("boş grafikte döngü")
	}
}

// Özellik: topolojik sıraya uyan rastgele grafik asla döngülü değildir;
// herhangi bir kenar tersine bir "geri kenar" eklenirse döngü mutlaka bulunur.
func TestDetectCycleProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 300; iter++ {
		n := 3 + rng.IntN(12)
		g := map[int64]Node{}
		for i := 2; i <= n; i++ { // ders i yalnızca daha küçük numaralı derslere bağlı
			var deps []Node
			for j := 1; j < i; j++ {
				if rng.IntN(3) == 0 {
					deps = append(deps, leaf(int64(j)))
				}
			}
			if len(deps) > 0 {
				g[int64(i)] = Node{Op: OpOr, Args: deps}
			}
		}
		if c := DetectCycle(g); c != nil {
			t.Fatalf("DAG'da sahte döngü: %v grafik=%v", c, g)
		}
		// Bir kenar zinciri bul ve sonunu başına bağla.
		for from, expr := range g {
			dep := expr.Courses()[0]
			// dep'ten from'a ulaşan bir yol yarat: dep → from
			old, had := g[dep]
			g[dep] = and(old, leaf(from))
			if !had {
				g[dep] = leaf(from)
			}
			if DetectCycle(g) == nil {
				t.Fatalf("geri kenar eklendi ama döngü bulunamadı: %v", g)
			}
			break
		}
	}
}

func meet(day, from, to int) Meeting { return Meeting{Day: day, StartMin: from, EndMin: to} }

func TestCheckReportsAllViolationsAtOnce(t *testing.T) {
	in := Input{
		GANO:   2.0, // limit 30
		Passed: set(10),
		CourseCodes: map[int64]string{
			1: "A", 2: "B", 3: "C", 4: "D", 10: "P",
		},
		Prereqs: map[int64]Node{2: and(leaf(10), leaf(11))}, // 11 geçilmemiş
		Current: []SectionInfo{
			{SectionID: 100, CourseID: 1, ECTS: 20, Meetings: []Meeting{meet(1, 540, 660)}},
		},
		Candidates: []SectionInfo{
			{SectionID: 201, CourseID: 2, ECTS: 6, Meetings: []Meeting{meet(1, 600, 720)}}, // 100 ile çakışır + ön koşul
			{SectionID: 202, CourseID: 1, ECTS: 6, Meetings: []Meeting{meet(2, 540, 600)}}, // aynı ders 1 (şube 100)
			{SectionID: 203, CourseID: 10, ECTS: 5},                                        // zaten geçilmiş
		},
	}
	rep := Check(in)
	got := map[string]int{}
	for _, v := range rep.Violations {
		got[v.Code]++
	}
	want := map[string]int{
		CodePrereqNotMet: 1, CodeTimeConflict: 1, CodeDuplicateCourse: 1, CodeAlreadyPassed: 1, CodeECTSLimit: 1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %d adet, beklenen %d (rapor: %+v)", k, got[k], v, rep.Violations)
		}
	}
	if len(rep.Violations) != 5 {
		t.Errorf("toplam ihlal %d, beklenen 5", len(rep.Violations))
	}
	if rep.TotalECTS != 37 || rep.MaxECTS != 30 {
		t.Errorf("AKTS %d/%d, beklenen 37/30", rep.TotalECTS, rep.MaxECTS)
	}
	// Çakışma raporu doğru şube çiftini göstermeli.
	for _, v := range rep.Violations {
		if v.Code == CodeTimeConflict && !slices.Equal(v.SectionIDs, []int64{100, 201}) {
			t.Errorf("çakışan şubeler %v, beklenen [100 201]", v.SectionIDs)
		}
	}
}

func TestCheckValidSchedule(t *testing.T) {
	in := Input{
		GANO: 3.2, Passed: set(1), CourseCodes: map[int64]string{1: "A", 2: "B"},
		Prereqs: map[int64]Node{2: leaf(1)},
		Candidates: []SectionInfo{
			{SectionID: 1, CourseID: 2, ECTS: 40, Meetings: []Meeting{meet(1, 540, 600)}},
		},
	}
	rep := Check(in)
	if !rep.OK() || rep.MaxECTS != 40 || rep.Violations == nil {
		t.Errorf("geçerli program reddedildi: %+v", rep)
	}
}

func TestCheckEdgeCases(t *testing.T) {
	// Bitişik saatler (10:00 bitiş / 10:00 başlangıç) çakışma değildir.
	in := Input{Candidates: []SectionInfo{
		{SectionID: 1, CourseID: 1, ECTS: 5, Meetings: []Meeting{meet(1, 540, 600)}},
		{SectionID: 2, CourseID: 2, ECTS: 5, Meetings: []Meeting{meet(1, 600, 660)}},
	}}
	if rep := Check(in); !rep.OK() {
		t.Errorf("bitişik saatler çakışma sayıldı: %+v", rep.Violations)
	}
	// Farklı günde aynı saat çakışma değildir.
	in.Candidates[1].Meetings = []Meeting{meet(2, 540, 600)}
	if rep := Check(in); !rep.OK() {
		t.Errorf("farklı gün çakışma sayıldı: %+v", rep.Violations)
	}
	// Aynı şube hem mevcut hem aday ise ikinci kez sayılmaz.
	s := SectionInfo{SectionID: 7, CourseID: 1, ECTS: 30}
	rep := Check(Input{Current: []SectionInfo{s}, Candidates: []SectionInfo{s}})
	if !rep.OK() || rep.TotalECTS != 30 {
		t.Errorf("mükerrer şube yanlış sayıldı: %+v", rep)
	}
	// Mevcut şubeler kendi aralarında çakışsa da yeni isteğin suçu değildir.
	cur := []SectionInfo{
		{SectionID: 1, CourseID: 1, Meetings: []Meeting{meet(1, 540, 600)}},
		{SectionID: 2, CourseID: 2, Meetings: []Meeting{meet(1, 540, 600)}},
	}
	if rep := Check(Input{Current: cur}); !rep.OK() {
		t.Errorf("geçmiş çakışma yeni isteğe yüklendi: %+v", rep.Violations)
	}
}

func TestMaxECTS(t *testing.T) {
	for _, c := range []struct {
		gano float64
		want int
	}{{0, 30}, {2.49, 30}, {2.5, 36}, {2.99, 36}, {3.0, 40}, {4.0, 40}} {
		if got := MaxECTS(c.gano); got != c.want {
			t.Errorf("MaxECTS(%.2f) = %d, beklenen %d", c.gano, got, c.want)
		}
	}
}

// Fuzz: dışarıdan gelen rastgele JSON, çözülüp doğrulanıp değerlendirilirken asla panik etmemeli.
func FuzzNodeJSON(f *testing.F) {
	f.Add(`{"op":"and","args":[{"course_id":1},{"op":"or","args":[{"course_id":2}]}]}`)
	f.Add(`{"op":"or"}`)
	f.Add(`[]`)
	f.Fuzz(func(t *testing.T, s string) {
		var n Node
		if json.Unmarshal([]byte(s), &n) != nil {
			return
		}
		if n.Validate() == nil {
			_ = n.Eval(set(1, 2, 3))
			_ = n.Describe(nil)
			_ = n.Courses()
		}
	})
}
