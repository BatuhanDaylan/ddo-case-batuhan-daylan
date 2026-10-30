package graduation

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ErrTooComplex havuz yapısı, çözücünün durum sınırını aştığında döner.
var ErrTooComplex = errors.New("havuz yapısı çok karmaşık: durum sınırı aşıldı")

// maxStates tek katmandaki erişilebilir durum sayısı sınırıdır (testlerde düşürülebilsin diye değişken).
var maxStates = 300_000

type Input struct {
	Rules     Rules
	Required  []string       // zorunlu dersler (kurallardan ya da müfredattan çözülmüş)
	Passed    map[string]int // geçilmiş sayılan ders kodu → AKTS (denklik/muafiyet dahil)
	TotalECTS int            // gerçekten başarılan toplam AKTS (her ders bir kez)
	GANO      float64
}

type PoolReport struct {
	Name           string   `json:"name"`
	MinCourses     int      `json:"min_courses"`
	MinECTS        int      `json:"min_ects"`
	Assigned       []string `json:"assigned"`
	AssignedECTS   int      `json:"assigned_ects"`
	MissingCourses int      `json:"missing_courses"`
	MissingECTS    int      `json:"missing_ects"`
	OK             bool     `json:"ok"`
	Remaining      []string `json:"remaining_candidates"` // havuzda olup henüz geçilmemiş dersler
}

type InternshipReport struct {
	Course string `json:"course"`
	OK     bool   `json:"ok"`
}

type Report struct {
	CanGraduate     bool              `json:"can_graduate"`
	GANO            float64           `json:"gano"`
	MinGANO         float64           `json:"min_gano"`
	GANOOK          bool              `json:"gano_ok"`
	TotalECTS       int               `json:"total_ects"`
	MinTotalECTS    int               `json:"min_total_ects"`
	ECTSOK          bool              `json:"ects_ok"`
	RequiredMissing []string          `json:"required_missing"`
	RequiredOK      bool              `json:"required_ok"`
	Pools           []PoolReport      `json:"pools"`
	Internship      *InternshipReport `json:"internship,omitempty"`
	Missing         []string          `json:"missing"` // insan okunur özet: neden mezun olamıyor
}

type cand struct {
	code  string
	ects  int
	pools []int
}

// Evaluate mezuniyet kurallarını değerlendirir.
//
// Bir ders birden çok havuzda geçerli olabilir ama yalnızca BİR havuza sayılır (ve zorunlu/staj
// olarak sayılan ders havuzlara girmez). En uygun atama aranır: açgözlü seçim burada yanlış sonuç
// verir (ortak dersi "dolu" havuza koymak, o dersten başka çıkışı olmayan havuzu boşta bırakır).
// Çözüm, her havuzun ilerlemesini (ders sayısı, AKTS) eşiğe kırparak izleyen katman katman
// dinamik programlamadır; durum sayısı ders sayısına değil havuz sayısına ve eşiklere bağlıdır.
// Hiçbir atama tüm havuzları doyurmuyorsa, toplam eksiği en küçük olan atama raporlanır.
func Evaluate(in Input) (Report, error) {
	rep := Report{
		GANO: in.GANO, MinGANO: in.Rules.MinGANO, GANOOK: in.GANO >= in.Rules.MinGANO,
		TotalECTS: in.TotalECTS, MinTotalECTS: in.Rules.MinTotalECTS, ECTSOK: in.TotalECTS >= in.Rules.MinTotalECTS,
		RequiredMissing: []string{}, Pools: []PoolReport{}, Missing: []string{},
	}

	// Zorunlu dersler.
	reserved := map[string]bool{}
	for _, c := range in.Required {
		reserved[c] = true
		if _, ok := in.Passed[c]; !ok {
			rep.RequiredMissing = append(rep.RequiredMissing, c)
		}
	}
	slices.Sort(rep.RequiredMissing)
	rep.RequiredOK = len(rep.RequiredMissing) == 0

	// Staj.
	if in.Rules.Internship != nil {
		c := in.Rules.Internship.Course
		reserved[c] = true
		_, ok := in.Passed[c]
		rep.Internship = &InternshipReport{Course: c, OK: ok}
	}

	// Havuzlara aday dersler: geçilmiş, zorunlu/staj olarak kullanılmayan ve en az bir havuzda geçerli.
	pools := in.Rules.Pools
	byCode := map[string]*cand{}
	for pi, p := range pools {
		for _, code := range p.Courses {
			ects, passed := in.Passed[code]
			if !passed || reserved[code] {
				continue
			}
			c := byCode[code]
			if c == nil {
				c = &cand{code: code, ects: ects}
				byCode[code] = c
			}
			c.pools = append(c.pools, pi)
		}
	}
	cands := make([]cand, 0, len(byCode))
	for _, c := range byCode {
		cands = append(cands, *c)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].code < cands[j].code })

	assigned, err := solve(pools, cands)
	if err != nil {
		return rep, err
	}

	for pi, p := range pools {
		pr := PoolReport{Name: p.Name, MinCourses: p.MinCourses, MinECTS: p.MinECTS, Assigned: []string{}, Remaining: []string{}}
		for _, code := range assigned[pi] {
			pr.Assigned = append(pr.Assigned, code)
			pr.AssignedECTS += in.Passed[code]
		}
		pr.MissingCourses = max(0, p.MinCourses-len(pr.Assigned))
		pr.MissingECTS = max(0, p.MinECTS-pr.AssignedECTS)
		pr.OK = pr.MissingCourses == 0 && pr.MissingECTS == 0
		for _, code := range p.Courses {
			if _, passed := in.Passed[code]; !passed {
				pr.Remaining = append(pr.Remaining, code)
			}
		}
		rep.Pools = append(rep.Pools, pr)
	}

	// Özet ve karar.
	poolsOK := true
	for _, p := range rep.Pools {
		poolsOK = poolsOK && p.OK
	}
	rep.CanGraduate = rep.GANOOK && rep.ECTSOK && rep.RequiredOK && poolsOK && (rep.Internship == nil || rep.Internship.OK)

	if !rep.RequiredOK {
		rep.Missing = append(rep.Missing, "Eksik zorunlu dersler: "+strings.Join(rep.RequiredMissing, ", "))
	}
	for _, p := range rep.Pools {
		if !p.OK {
			msg := fmt.Sprintf("%s havuzu: %d ders ve %d AKTS daha gerekli", p.Name, p.MissingCourses, p.MissingECTS)
			if len(p.Remaining) > 0 {
				msg += " (alınabilecek dersler: " + strings.Join(p.Remaining, ", ") + ")"
			}
			rep.Missing = append(rep.Missing, msg)
		}
	}
	if rep.Internship != nil && !rep.Internship.OK {
		rep.Missing = append(rep.Missing, "Staj dersi tamamlanmamış: "+rep.Internship.Course)
	}
	if !rep.GANOOK {
		rep.Missing = append(rep.Missing, fmt.Sprintf("GANO %.2f, gereken en az %.2f", rep.GANO, rep.MinGANO))
	}
	if !rep.ECTSOK {
		rep.Missing = append(rep.Missing, fmt.Sprintf("Toplam AKTS %d, gereken en az %d", rep.TotalECTS, rep.MinTotalECTS))
	}
	return rep, nil
}

type node struct {
	prev string
	pool int // -1: bu ders hiçbir havuza atanmadı
}

// solve, her aday dersi bir havuza (ya da hiçbirine) atayarak toplam eksiği en aza indirir.
// Dönüş: havuz indeksine göre atanan ders kodları (sıralı).
func solve(pools []Pool, cands []cand) ([][]string, error) {
	np := len(pools)
	out := make([][]string, np)
	if np == 0 || len(cands) == 0 {
		return out, nil
	}

	// Durum: havuz başına (karşılanan ders sayısı, karşılanan AKTS), ikisi de eşiğe kırpılır.
	encode := func(counts, ects []int) string {
		b := make([]byte, 0, 3*np)
		for i := 0; i < np; i++ {
			b = append(b, byte(counts[i]), byte(ects[i]>>8), byte(ects[i]))
		}
		return string(b)
	}
	decode := func(key string) (counts, ects []int) {
		counts, ects = make([]int, np), make([]int, np)
		for i := 0; i < np; i++ {
			counts[i] = int(key[3*i])
			ects[i] = int(key[3*i+1])<<8 | int(key[3*i+2])
		}
		return
	}

	zero := encode(make([]int, np), make([]int, np))
	layers := make([]map[string]node, len(cands)+1)
	layers[0] = map[string]node{zero: {prev: "", pool: -1}}

	for i, c := range cands {
		cur := layers[i]
		next := make(map[string]node, len(cur))
		keys := make([]string, 0, len(cur))
		for k := range cur {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministik sonuç

		for _, k := range keys {
			if _, ok := next[k]; !ok {
				next[k] = node{prev: k, pool: -1} // bu dersi hiçbir havuza koyma
			}
			counts, ects := decode(k)
			for _, pi := range c.pools {
				p := pools[pi]
				if counts[pi] >= p.MinCourses && ects[pi] >= p.MinECTS {
					continue // havuz zaten doymuş: ders eklemenin faydası yok
				}
				nc, ne := slices.Clone(counts), slices.Clone(ects)
				nc[pi] = min(nc[pi]+1, p.MinCourses)
				ne[pi] = min(ne[pi]+c.ects, p.MinECTS)
				nk := encode(nc, ne)
				if _, ok := next[nk]; !ok {
					next[nk] = node{prev: k, pool: pi}
				}
			}
		}
		if len(next) > maxStates {
			return nil, ErrTooComplex
		}
		layers[i+1] = next
	}

	// En küçük toplam eksikli son durumu seç.
	last := layers[len(cands)]
	keys := make([]string, 0, len(last))
	for k := range last {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, bestDef := "", int(^uint(0)>>1)
	for _, k := range keys {
		counts, ects := decode(k)
		def := 0
		for pi, p := range pools {
			def += (p.MinECTS-ects[pi])*1000 + (p.MinCourses - counts[pi])
		}
		if def < bestDef {
			best, bestDef = k, def
		}
	}

	// Atamayı geriye doğru çıkar.
	key := best
	for i := len(cands); i >= 1; i-- {
		nd := layers[i][key]
		if nd.pool >= 0 {
			out[nd.pool] = append(out[nd.pool], cands[i-1].code)
		}
		key = nd.prev
	}
	for pi := range out {
		slices.Sort(out[pi])
	}
	return out, nil
}
