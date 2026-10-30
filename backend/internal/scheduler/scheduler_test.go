package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

func solve(t *testing.T, p Problem, d time.Duration) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	r, err := Solve(ctx, p, Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func kinds(r Result) []string {
	var out []string
	for _, c := range r.Conflicts {
		out = append(out, c.Kind)
	}
	return out
}

// Şartname örneği: 60 ders / 12 derslik / 20 öğretim üyesi, 30 saniye içinde çakışmasız program.
func TestSampleSolvesQuicklyAndIsValid(t *testing.T) {
	for seed := int64(1); seed <= 10; seed++ {
		p := Sample(seed)
		if len(p.Events) != 60 || len(p.Rooms) != 12 || len(p.Instructors) != 20 {
			t.Fatalf("örnek ölçeği yanlış: %d/%d/%d", len(p.Events), len(p.Rooms), len(p.Instructors))
		}
		start := time.Now()
		r := solve(t, p, 30*time.Second)
		if r.Status != StatusSolved {
			t.Fatalf("tohum %d: durum %s, çatışmalar %v", seed, r.Status, r.Conflicts)
		}
		if errs := Check(p, r.Placements); len(errs) != 0 {
			t.Fatalf("tohum %d: geçersiz program: %v", seed, errs)
		}
		if el := time.Since(start); el > 10*time.Second {
			t.Errorf("tohum %d: %v sürdü (hedef 30 sn'nin çok altı)", seed, el)
		}
	}
}

func TestSolveIsDeterministic(t *testing.T) {
	p := Sample(3)
	a, b := solve(t, p, 30*time.Second), solve(t, p, 30*time.Second)
	if fmt.Sprint(a.Placements) != fmt.Sprint(b.Placements) {
		t.Error("aynı girdi farklı program üretti")
	}
}

func baseProblem() Problem {
	return Problem{
		Config:      Config{Days: 2, SlotsPerDay: 4},
		Rooms:       []Room{{ID: "R1", Capacity: 40, Kind: KindLecture}, {ID: "L1", Capacity: 20, Kind: KindLab}},
		Instructors: []Instructor{{ID: "I1"}, {ID: "I2"}},
	}
}

func TestStaticConflictsAreNamed(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(p *Problem)
		wants string
	}{
		{"uygun derslik yok (kapasite)", func(p *Problem) {
			p.Events = []Event{{ID: "E1", Name: "Büyük", Instructor: "I1", Students: 500, Kind: KindLecture, Duration: 2}}
		}, ConflictNoRoom},
		{"lab yok", func(p *Problem) {
			p.Rooms = p.Rooms[:1]
			p.Events = []Event{{ID: "E1", Name: "Lab", Instructor: "I1", Students: 10, Kind: KindLab, Duration: 2}}
		}, ConflictNoRoom},
		{"öğretim üyesi ardışık müsait aralığı yok", func(p *Problem) {
			p.Instructors[0].Unavailable = []int{1, 5} // her gün ortadan bölünmüş: 2 saatlik ardışık yer kalmıyor
			p.Events = []Event{{ID: "E1", Name: "X", Instructor: "I1", Students: 10, Kind: KindLecture, Duration: 4}}
		}, ConflictInstructorAvail},
		{"öğretim üyesi aşırı yük", func(p *Problem) {
			for i := 0; i < 3; i++ {
				p.Events = append(p.Events, Event{ID: fmt.Sprint("E", i), Name: "X", Instructor: "I1", Students: 10, Kind: KindLecture, Duration: 4})
			}
		}, ConflictInstructorOverload},
		{"grup aşırı yük", func(p *Problem) {
			for i := 0; i < 3; i++ {
				p.Events = append(p.Events, Event{ID: fmt.Sprint("E", i), Name: "X", Students: 10, Kind: KindLecture, Duration: 4, Groups: []string{"G1"}})
			}
		}, ConflictGroupOverload},
		{"derslik kapasitesi (lab)", func(p *Problem) {
			for i := 0; i < 3; i++ { // 1 lab × 8 saat = 8 saat; talep 12 saat
				p.Events = append(p.Events, Event{ID: fmt.Sprint("L", i), Name: "Lab", Students: 10, Kind: KindLab, Duration: 4})
			}
		}, ConflictRoomCapacity},
	}
	for _, c := range cases {
		p := baseProblem()
		c.mut(&p)
		r := solve(t, p, 5*time.Second)
		if r.Status != StatusInfeasible || !slices.Contains(kinds(r), c.wants) {
			t.Errorf("%s: durum %s, çatışmalar %v; beklenen %s", c.name, r.Status, kinds(r), c.wants)
		}
		for _, cf := range r.Conflicts {
			if cf.Message == "" {
				t.Errorf("%s: açıklamasız çatışma", c.name)
			}
		}
	}
}

// Statik analizin yakalayamadığı, ancak arama ile kanıtlanan çözümsüzlük: iki öğretim üyesi de yalnızca
// aynı tek saatte müsait ve tek derslik var.
func TestSearchProvesInfeasibility(t *testing.T) {
	p := Problem{
		Config:      Config{Days: 1, SlotsPerDay: 2},
		Rooms:       []Room{{ID: "R1", Capacity: 50, Kind: KindLecture}},
		Instructors: []Instructor{{ID: "I1", Unavailable: []int{1}}, {ID: "I2", Unavailable: []int{1}}},
		Events: []Event{
			{ID: "A", Name: "A", Instructor: "I1", Students: 10, Kind: KindLecture, Duration: 1},
			{ID: "B", Name: "B", Instructor: "I2", Students: 10, Kind: KindLecture, Duration: 1},
		},
	}
	r := solve(t, p, 5*time.Second)
	if r.Status != StatusInfeasible || !slices.Contains(kinds(r), ConflictSearch) {
		t.Fatalf("durum %s, çatışmalar %v", r.Status, r.Conflicts)
	}
	if !strings.Contains(r.Conflicts[0].Message, "derslik") && !strings.Contains(r.Conflicts[0].Message, "çakış") {
		t.Errorf("açıklama nedeni söylemiyor: %s", r.Conflicts[0].Message)
	}
}

func TestExpiredContextTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Solve(ctx, Sample(2), Options{Seed: 1})
	if err != nil || r.Status != StatusTimeout || len(r.Conflicts) == 0 {
		t.Errorf("durum=%s err=%v çatışma=%v", r.Status, err, r.Conflicts)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	good := baseProblem()
	good.Events = []Event{{ID: "E", Instructor: "I1", Students: 5, Kind: KindLecture, Duration: 1}}
	if err := good.Validate(); err != nil {
		t.Fatalf("geçerli problem reddedildi: %v", err)
	}
	mutations := map[string]func(p *Problem){
		"çok büyük ızgara":      func(p *Problem) { p.Config = Config{Days: 10, SlotsPerDay: 10} },
		"sıfır gün":             func(p *Problem) { p.Config.Days = 0 },
		"derslik tekrar":        func(p *Problem) { p.Rooms = append(p.Rooms, p.Rooms[0]) },
		"derslik türü":          func(p *Problem) { p.Rooms[0].Kind = "x" },
		"derslik kapasitesi":    func(p *Problem) { p.Rooms[0].Capacity = 0 },
		"müsait olmayan aralık": func(p *Problem) { p.Instructors[0].Unavailable = []int{99} },
		"öğretim üyesi tekrar":  func(p *Problem) { p.Instructors = append(p.Instructors, p.Instructors[0]) },
		"süre sınır":            func(p *Problem) { p.Events[0].Duration = 9 },
		"bilinmeyen öğretim":    func(p *Problem) { p.Events[0].Instructor = "yok" },
		"etkinlik tekrar":       func(p *Problem) { p.Events = append(p.Events, p.Events[0]) },
		"etkinlik türü":         func(p *Problem) { p.Events[0].Kind = "z" },
	}
	for name, m := range mutations {
		p := good
		p.Rooms = slices.Clone(good.Rooms)
		p.Instructors = slices.Clone(good.Instructors)
		p.Events = slices.Clone(good.Events)
		m(&p)
		if p.Validate() == nil {
			t.Errorf("%s: geçersiz girdi kabul edildi", name)
		}
		if _, err := Solve(context.Background(), p, Options{}); err == nil {
			t.Errorf("%s: Solve geçersiz girdiyi çözmeye çalıştı", name)
		}
	}
}

func TestCheckDetectsEveryViolation(t *testing.T) {
	p := baseProblem()
	p.Instructors[0].Unavailable = []int{0}
	p.Events = []Event{
		{ID: "A", Instructor: "I1", Students: 10, Kind: KindLecture, Duration: 2, Groups: []string{"G"}},
		{ID: "B", Instructor: "I1", Students: 10, Kind: KindLecture, Duration: 2, Groups: []string{"G"}},
		{ID: "C", Instructor: "I2", Students: 30, Kind: KindLab, Duration: 1},
	}
	pl := func(id string, day, slot, d int, room string) Placement {
		return Placement{EventID: id, Day: day, Slot: slot, Duration: d, RoomID: room}
	}
	bad := []Placement{pl("A", 0, 0, 2, "R1"), pl("B", 0, 1, 2, "R1"), pl("C", 0, 1, 1, "L1"), pl("C", 0, 3, 1, "L1")}
	errs := strings.Join(Check(p, bad), "\n")
	for _, want := range []string{"müsait değil", "aynı derslikte", "aynı öğretim üyesinde", "aynı grupta", "uygun değil", "birden fazla"} {
		if !strings.Contains(errs, want) {
			t.Errorf("denetçi %q ihlalini yakalamadı:\n%s", want, errs)
		}
	}
	if e := Check(p, nil); len(e) != 3 {
		t.Errorf("yerleştirilmemiş etkinlikler: %v", e)
	}
	if e := Check(p, []Placement{pl("A", 5, 0, 2, "R1"), pl("B", 0, 3, 2, "R1"), pl("C", 0, 0, 1, "YOK")}); len(e) < 3 {
		t.Errorf("aralık/derslik hataları yakalanmadı: %v", e)
	}
}

// Özellik: küçük rastgele problemlerde çözücü kaba kuvvetle uyumludur:
// "solved" dediyse program geçerlidir; "infeasible" dediyse kaba kuvvet de çözüm bulamaz.
func TestSolverAgreesWithBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	solved, infeasible := 0, 0
	for iter := 0; iter < 400; iter++ {
		p := Problem{Config: Config{Days: 1 + rng.IntN(2), SlotsPerDay: 2 + rng.IntN(2)}}
		for r := 0; r < 1+rng.IntN(2); r++ {
			kind := []string{KindLecture, KindLecture, KindLab}[rng.IntN(3)]
			p.Rooms = append(p.Rooms, Room{ID: fmt.Sprint("R", r), Capacity: 10 + rng.IntN(30), Kind: kind})
		}
		for i := 0; i < 1+rng.IntN(2); i++ {
			in := Instructor{ID: fmt.Sprint("I", i)}
			for k := 0; k < rng.IntN(3); k++ {
				in.Unavailable = append(in.Unavailable, rng.IntN(p.Config.Slots()))
			}
			p.Instructors = append(p.Instructors, in)
		}
		for e := 0; e < 2+rng.IntN(3); e++ {
			ev := Event{ID: fmt.Sprint("E", e), Name: "x", Students: 5 + rng.IntN(30), Duration: 1 + rng.IntN(2),
				Kind: []string{KindLecture, KindLecture, KindLab}[rng.IntN(3)], Instructor: p.Instructors[rng.IntN(len(p.Instructors))].ID}
			if rng.IntN(2) == 0 {
				ev.Groups = []string{fmt.Sprint("G", rng.IntN(2))}
			}
			if ev.Duration > p.Config.SlotsPerDay {
				ev.Duration = p.Config.SlotsPerDay
			}
			p.Events = append(p.Events, ev)
		}

		r := solve(t, p, 10*time.Second)
		exists := bruteForce(p)
		switch r.Status {
		case StatusSolved:
			solved++
			if errs := Check(p, r.Placements); len(errs) != 0 {
				t.Fatalf("iter %d: çözücü geçersiz program üretti: %v", iter, errs)
			}
			if !exists {
				t.Fatalf("iter %d: çözücü çözdü ama kaba kuvvet çözüm bulamadı", iter)
			}
		case StatusInfeasible:
			infeasible++
			if exists {
				t.Fatalf("iter %d: çözücü 'çözümsüz' dedi ama kaba kuvvet çözüm buldu (%+v)", iter, p)
			}
			if len(r.Conflicts) == 0 {
				t.Fatalf("iter %d: çözümsüzlük açıklamasız", iter)
			}
		default:
			t.Fatalf("iter %d: beklenmeyen durum %s", iter, r.Status)
		}
	}
	if solved < 30 || infeasible < 30 {
		t.Errorf("test dengesiz: çözülen=%d çözümsüz=%d", solved, infeasible)
	}
}

func bruteForce(p Problem) bool {
	cfg := p.Config
	type val struct {
		t int
		r string
	}
	domains := make([][]val, len(p.Events))
	for i, e := range p.Events {
		for day := 0; day < cfg.Days; day++ {
			for slot := 0; slot+e.Duration <= cfg.SlotsPerDay; slot++ {
				for _, r := range p.Rooms {
					if roomFits(e, r) {
						domains[i] = append(domains[i], val{day*cfg.SlotsPerDay + slot, r.ID})
					}
				}
			}
		}
	}
	pls := make([]Placement, len(p.Events))
	var rec func(i int) bool
	rec = func(i int) bool {
		if i == len(p.Events) {
			return len(Check(p, pls)) == 0
		}
		for _, v := range domains[i] {
			pls[i] = Placement{EventID: p.Events[i].ID, Day: v.t / cfg.SlotsPerDay, Slot: v.t % cfg.SlotsPerDay, Duration: p.Events[i].Duration, RoomID: v.r}
			if rec(i + 1) {
				return true
			}
		}
		return false
	}
	return rec(0)
}
