package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
)

// Options çözücü ayarları.
type Options struct {
	Seed int64 // aynı tohum aynı sonucu verir (deterministik)
}

// Solve programı üretir. ctx süresi dolarsa StatusTimeout döner.
//
// Yöntem: kısıt tatmin problemi için geri izleme araması.
//   - Değişken seçimi "en kısıtlı önce" (MRV): her düğümde, geçerli (saat, derslik) çifti en az olan
//     yerleştirilmemiş etkinlik seçilir; çifti kalmayan etkinlik varsa o dal hemen kesilir (ileriye dönük denetim).
//   - Çakışma denetimi bit maskeleriyle yapılır: derslik/öğretim üyesi/grup başına haftalık saatler tek bir uint64'tür.
//   - Aramalar giderek büyüyen düğüm sınırıyla ve değişen eşitlik bozma sırasıyla yeniden başlatılır
//     (zor örneklerde takılmayı önler). Bir arama SINIRA TAKILMADAN biterse çözümsüzlük kanıtlanmış olur.
func Solve(ctx context.Context, p Problem, opt Options) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, err
	}
	s := newSolver(p)

	// Önce ucuz ve kesin analiz: tek başına çözümsüzlüğü kanıtlayan nedenler.
	if static := analyze(p, s); len(static) > 0 {
		return Result{Status: StatusInfeasible, Conflicts: static}, nil
	}

	res := Result{}
	limit := 4000
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			res.Status, res.Conflicts = StatusTimeout, s.searchConflicts("Süre doldu; çözüm bulunamadı. En çok zorlanan etkinlikler:")
			return res, nil
		}
		s.reset(rand.New(rand.NewPCG(uint64(opt.Seed), uint64(attempt)+1)), attempt > 0)
		s.nodeLimit = limit
		res.Restarts = attempt
		switch s.dfs(ctx, len(p.Events)) {
		case found:
			res.Status, res.Nodes = StatusSolved, s.nodes
			res.Placements = s.placements()
			return res, nil
		case exhausted:
			// Sınıra takılmadan tüm ağaç tarandı: çözüm yok.
			res.Status, res.Nodes = StatusInfeasible, s.nodes
			res.Conflicts = s.searchConflicts("Tüm atamalar denendi; kısıtların birleşimi çözümsüz. En çok zorlanan etkinlikler:")
			return res, nil
		case aborted: // süre doldu
			res.Status, res.Nodes = StatusTimeout, s.nodes
			res.Conflicts = s.searchConflicts("Süre doldu; çözüm bulunamadı. En çok zorlanan etkinlikler:")
			return res, nil
		case limitHit:
			res.Nodes += s.nodes
			limit *= 2
		}
	}
}

type outcome int

const (
	found outcome = iota
	exhausted
	limitHit
	aborted
)

type failure struct {
	count              int
	instr, group, room int // engellenen (saat, derslik) çiftlerinin nedenleri
}

type solver struct {
	p          Problem
	n          int
	compat     [][]int // etkinlik → uygun derslik indeksleri (kapasiteye göre artan)
	starts     [][]int // etkinlik → öğretim üyesi müsaitliğine göre geçerli başlangıç saatleri
	instrIdx   []int   // etkinlik → öğretim üyesi indeksi (-1: yok)
	groups     [][]int // etkinlik → grup indeksleri
	groupNames []string

	roomBusy, instrBusy, groupBusy []uint64
	assigned                       []bool
	placeT                         []int
	placeR                         []int
	tie                            []int // eşitlik bozma anahtarı (yeniden başlatmada karıştırılır)
	startOrd                       [][]int

	nodes, nodeLimit int
	fails            []failure
}

func newSolver(p Problem) *solver {
	cfg := p.Config
	s := &solver{p: p, n: len(p.Events)}
	instrIdx := map[string]int{}
	unavail := make([]uint64, len(p.Instructors))
	for i, in := range p.Instructors {
		instrIdx[in.ID] = i
		for _, t := range in.Unavailable {
			unavail[i] |= 1 << uint(t)
		}
	}
	groupIdx := map[string]int{}
	s.compat = make([][]int, s.n)
	s.starts = make([][]int, s.n)
	s.instrIdx = make([]int, s.n)
	s.groups = make([][]int, s.n)
	for ei, e := range p.Events {
		for ri, r := range p.Rooms {
			if roomFits(e, r) {
				s.compat[ei] = append(s.compat[ei], ri)
			}
		}
		sort.SliceStable(s.compat[ei], func(a, b int) bool {
			return p.Rooms[s.compat[ei][a]].Capacity < p.Rooms[s.compat[ei][b]].Capacity
		})
		s.instrIdx[ei] = -1
		if e.Instructor != "" {
			s.instrIdx[ei] = instrIdx[e.Instructor]
		}
		for day := 0; day < cfg.Days; day++ {
			for slot := 0; slot+e.Duration <= cfg.SlotsPerDay; slot++ {
				t := day*cfg.SlotsPerDay + slot
				if s.instrIdx[ei] >= 0 && unavail[s.instrIdx[ei]]&mask(t, e.Duration) != 0 {
					continue
				}
				s.starts[ei] = append(s.starts[ei], t)
			}
		}
		for _, g := range e.Groups {
			gi, ok := groupIdx[g]
			if !ok {
				gi = len(s.groupNames)
				groupIdx[g] = gi
				s.groupNames = append(s.groupNames, g)
			}
			s.groups[ei] = append(s.groups[ei], gi)
		}
	}
	return s
}

func mask(t, d int) uint64 { return ((uint64(1) << uint(d)) - 1) << uint(t) }

func (s *solver) reset(rng *rand.Rand, shuffle bool) {
	s.roomBusy = make([]uint64, len(s.p.Rooms))
	s.instrBusy = make([]uint64, len(s.p.Instructors))
	s.groupBusy = make([]uint64, len(s.groupNames))
	s.assigned = make([]bool, s.n)
	s.placeT = make([]int, s.n)
	s.placeR = make([]int, s.n)
	s.tie = make([]int, s.n)
	s.startOrd = make([][]int, s.n)
	for i := range s.tie {
		s.tie[i] = i
		s.startOrd[i] = slices.Clone(s.starts[i])
	}
	if shuffle {
		rng.Shuffle(s.n, func(a, b int) { s.tie[a], s.tie[b] = s.tie[b], s.tie[a] })
		for i := range s.startOrd {
			rng.Shuffle(len(s.startOrd[i]), func(a, b int) { s.startOrd[i][a], s.startOrd[i][b] = s.startOrd[i][b], s.startOrd[i][a] })
		}
	}
	s.nodes = 0
	if s.fails == nil {
		s.fails = make([]failure, s.n)
	}
}

// valid (t, r) çiftinin şu an boş olup olmadığını söyler.
func (s *solver) timeFree(e, t int) bool {
	m := mask(t, s.p.Events[e].Duration)
	if i := s.instrIdx[e]; i >= 0 && s.instrBusy[i]&m != 0 {
		return false
	}
	for _, g := range s.groups[e] {
		if s.groupBusy[g]&m != 0 {
			return false
		}
	}
	return true
}

func (s *solver) domainSize(e int) int {
	size := 0
	d := s.p.Events[e].Duration
	for _, t := range s.starts[e] {
		if !s.timeFree(e, t) {
			continue
		}
		m := mask(t, d)
		for _, r := range s.compat[e] {
			if s.roomBusy[r]&m == 0 {
				size++
			}
		}
	}
	return size
}

// recordFailure alanı boşalan etkinliğin neden yerleştirilemediğini sayar (çatışma raporu için).
func (s *solver) recordFailure(e int) {
	f := &s.fails[e]
	f.count++
	d := s.p.Events[e].Duration
	for _, t := range s.starts[e] {
		m := mask(t, d)
		instrBlocked := s.instrIdx[e] >= 0 && s.instrBusy[s.instrIdx[e]]&m != 0
		groupBlocked := false
		for _, g := range s.groups[e] {
			groupBlocked = groupBlocked || s.groupBusy[g]&m != 0
		}
		for _, r := range s.compat[e] {
			switch {
			case instrBlocked:
				f.instr++
			case groupBlocked:
				f.group++
			case s.roomBusy[r]&m != 0:
				f.room++
			}
		}
	}
}

func (s *solver) dfs(ctx context.Context, remaining int) outcome {
	if remaining == 0 {
		return found
	}
	s.nodes++
	if s.nodes&255 == 0 && ctx.Err() != nil {
		return aborted
	}
	if s.nodes > s.nodeLimit {
		return limitHit
	}

	// En kısıtlı etkinliği seç; alanı boş olan varsa dalı kes.
	best, bestSize := -1, int(^uint(0)>>1)
	for e := 0; e < s.n; e++ {
		if s.assigned[e] {
			continue
		}
		size := s.domainSize(e)
		if size == 0 {
			s.recordFailure(e)
			return exhausted
		}
		better := size < bestSize ||
			(size == bestSize && (s.p.Events[e].Duration > s.p.Events[best].Duration ||
				(s.p.Events[e].Duration == s.p.Events[best].Duration && s.tie[e] < s.tie[best])))
		if better {
			best, bestSize = e, size
		}
	}

	e := best
	d := s.p.Events[e].Duration
	for _, t := range s.startOrd[e] {
		if !s.timeFree(e, t) {
			continue
		}
		m := mask(t, d)
		for _, r := range s.compat[e] {
			if s.roomBusy[r]&m != 0 {
				continue
			}
			s.apply(e, t, r, m)
			switch s.dfs(ctx, remaining-1) {
			case found:
				return found
			case aborted:
				s.undo(e, t, r, m)
				return aborted
			case limitHit:
				s.undo(e, t, r, m)
				return limitHit // sınıra takıldık: bu aramayı bırak (yeniden başlatılacak)
			}
			s.undo(e, t, r, m)
		}
	}
	return exhausted
}

func (s *solver) apply(e, t, r int, m uint64) {
	s.assigned[e], s.placeT[e], s.placeR[e] = true, t, r
	s.roomBusy[r] |= m
	if i := s.instrIdx[e]; i >= 0 {
		s.instrBusy[i] |= m
	}
	for _, g := range s.groups[e] {
		s.groupBusy[g] |= m
	}
}

func (s *solver) undo(e, t, r int, m uint64) {
	s.assigned[e] = false
	s.roomBusy[r] &^= m
	if i := s.instrIdx[e]; i >= 0 {
		s.instrBusy[i] &^= m
	}
	for _, g := range s.groups[e] {
		s.groupBusy[g] &^= m
	}
}

func (s *solver) placements() []Placement {
	cfg := s.p.Config
	out := make([]Placement, 0, s.n)
	for e, ev := range s.p.Events {
		t := s.placeT[e]
		out = append(out, Placement{
			EventID: ev.ID, Day: t / cfg.SlotsPerDay, Slot: t % cfg.SlotsPerDay,
			Duration: ev.Duration, RoomID: s.p.Rooms[s.placeR[e]].ID,
		})
	}
	return out
}

// searchConflicts en çok alanı boşalan etkinlikleri ve engelleyen kısıt türlerini açıklar.
// Bu, aramadan türetilmiş bir YAKLAŞIK açıklamadır; kesin nedenler analyze() ile ayrıca bulunur.
func (s *solver) searchConflicts(header string) []Conflict {
	type ranked struct {
		e int
		f failure
	}
	var rs []ranked
	for e, f := range s.fails {
		if f.count > 0 {
			rs = append(rs, ranked{e, f})
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].f.count != rs[j].f.count {
			return rs[i].f.count > rs[j].f.count
		}
		return rs[i].e < rs[j].e
	})
	if len(rs) > 3 {
		rs = rs[:3]
	}
	if len(rs) == 0 {
		return []Conflict{{Kind: ConflictSearch, Message: header + " (ayrıntı toplanamadı)"}}
	}
	out := make([]Conflict, 0, len(rs))
	for _, r := range rs {
		ev := s.p.Events[r.e]
		parts := []string{}
		if r.f.instr > 0 {
			parts = append(parts, fmt.Sprintf("öğretim üyesi çakışması (%s)", ev.Instructor))
		}
		if r.f.group > 0 {
			parts = append(parts, fmt.Sprintf("grup çakışması (%s)", strings.Join(ev.Groups, ", ")))
		}
		if r.f.room > 0 {
			parts = append(parts, "uygun derslik doluluğu")
		}
		out = append(out, Conflict{
			Kind:    ConflictSearch,
			Message: fmt.Sprintf("%s %s (%s) birçok denemede yerleştirilemedi; engelleyenler: %s", header, ev.ID, ev.Name, strings.Join(parts, ", ")),
			Events:  []string{ev.ID},
		})
	}
	return out
}
