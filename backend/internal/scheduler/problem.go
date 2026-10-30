// Package scheduler ders programı çözücüsüdür (saf, veritabanından bağımsız).
//
// Problem bir kısıt tatmin problemidir: her etkinlik (şube) için bir başlangıç saati ve bir derslik seçilir.
// Kısıtlar: derslik kapasitesi ve türü, öğretim üyesi müsaitliği, öğretim üyesi aynı anda iki yerde olamaz,
// aynı derslikte iki etkinlik çakışamaz, aynı gruptaki (örn. aynı yarıyılın zorunlu dersleri) etkinlikler çakışamaz.
package scheduler

import (
	"errors"
	"fmt"
	"slices"
	"sort"
)

// Derslik türleri. Etkinlik türü: lab yalnızca lab'a, amphi yalnızca amfiye, lecture sınıfa VEYA amfiye girer.
const (
	KindLecture = "lecture"
	KindLab     = "lab"
	KindAmphi   = "amphi"
)

type Config struct {
	Days        int // haftalık gün sayısı
	SlotsPerDay int // gün başına ders saati sayısı
}

// Slots toplam haftalık saat sayısıdır.
func (c Config) Slots() int { return c.Days * c.SlotsPerDay }

type Room struct {
	ID       string
	Capacity int
	Kind     string
}

type Instructor struct {
	ID          string
	Unavailable []int // müsait olmadığı saatler: gün*SlotsPerDay + saat
}

type Event struct {
	ID         string
	Name       string
	Instructor string   // boşsa öğretim üyesi kısıtı yok
	Students   int      // gereken kapasite
	Kind       string   // KindLecture | KindLab | KindAmphi
	Duration   int      // ardışık saat sayısı (aynı gün içinde)
	Groups     []string // bu gruplardaki etkinlikler birbiriyle çakışamaz
}

type Problem struct {
	Config      Config
	Rooms       []Room
	Instructors []Instructor
	Events      []Event
}

type Placement struct {
	EventID  string
	Day      int // 0'dan
	Slot     int // gün içindeki başlangıç saati (0'dan)
	Duration int
	RoomID   string
}

// Başlangıç saati (haftalık indeks).
func (p Placement) start(cfg Config) int { return p.Day*cfg.SlotsPerDay + p.Slot }

// Sonuç durumları.
const (
	StatusSolved     = "solved"
	StatusInfeasible = "infeasible" // çözümsüz (tam arama ile ya da analizle kanıtlandı)
	StatusTimeout    = "timeout"    // süre doldu; çözüm bulunamadı (çözümsüz olduğu kanıtlanmadı)
)

// Çatışma türleri.
const (
	ConflictNoRoom             = "NO_ROOM"
	ConflictInstructorAvail    = "INSTRUCTOR_UNAVAILABLE"
	ConflictInstructorOverload = "INSTRUCTOR_OVERLOAD"
	ConflictGroupOverload      = "GROUP_OVERLOAD"
	ConflictRoomCapacity       = "ROOM_CAPACITY_OVERLOAD"
	ConflictSearch             = "SEARCH_CONFLICT"
)

// Conflict çözümsüzlüğün (ya da çözüm bulunamamasının) nedenini açıklar.
type Conflict struct {
	Kind    string   `json:"kind"`
	Message string   `json:"message"`
	Events  []string `json:"events,omitempty"`
}

type Result struct {
	Status     string
	Placements []Placement
	Conflicts  []Conflict
	Nodes      int
	Restarts   int
}

// Validate girdinin yapısal olarak tutarlı olduğunu denetler.
func (p Problem) Validate() error {
	c := p.Config
	if c.Days < 1 || c.SlotsPerDay < 1 || c.Slots() > 64 {
		return errors.New("days ve slots_per_day pozitif olmalı; toplam saat en fazla 64 olabilir")
	}
	rooms := map[string]bool{}
	for _, r := range p.Rooms {
		if r.ID == "" || rooms[r.ID] || r.Capacity < 1 || !validKind(r.Kind) {
			return fmt.Errorf("derslik %q geçersiz veya tekrar ediyor", r.ID)
		}
		rooms[r.ID] = true
	}
	instr := map[string]bool{}
	for _, i := range p.Instructors {
		if i.ID == "" || instr[i.ID] {
			return fmt.Errorf("öğretim üyesi %q geçersiz veya tekrar ediyor", i.ID)
		}
		instr[i.ID] = true
		for _, t := range i.Unavailable {
			if t < 0 || t >= c.Slots() {
				return fmt.Errorf("öğretim üyesi %s: müsait olmayan saat %d aralık dışı", i.ID, t)
			}
		}
	}
	events := map[string]bool{}
	for _, e := range p.Events {
		switch {
		case e.ID == "" || events[e.ID]:
			return fmt.Errorf("etkinlik %q geçersiz veya tekrar ediyor", e.ID)
		case e.Duration < 1 || e.Duration > c.SlotsPerDay:
			return fmt.Errorf("etkinlik %s: süre 1-%d saat olmalı", e.ID, c.SlotsPerDay)
		case e.Students < 0 || !validKind(e.Kind):
			return fmt.Errorf("etkinlik %s: öğrenci sayısı/tür geçersiz", e.ID)
		case e.Instructor != "" && !instr[e.Instructor]:
			return fmt.Errorf("etkinlik %s: bilinmeyen öğretim üyesi %q", e.ID, e.Instructor)
		}
		events[e.ID] = true
	}
	return nil
}

func validKind(k string) bool { return k == KindLecture || k == KindLab || k == KindAmphi }

// roomFits etkinliğin dersliğe girip giremeyeceğini söyler (kapasite + tür).
func roomFits(e Event, r Room) bool {
	if r.Capacity < e.Students {
		return false
	}
	switch e.Kind {
	case KindLab:
		return r.Kind == KindLab
	case KindAmphi:
		return r.Kind == KindAmphi
	default: // lecture: sınıf ya da amfi
		return r.Kind == KindLecture || r.Kind == KindAmphi
	}
}

// Check bir çözümün tüm kısıtları sağladığını ÇÖZÜCÜDEN BAĞIMSIZ yazılmış kodla doğrular.
// Hata listesi boşsa çözüm geçerlidir.
func Check(p Problem, placements []Placement) []string {
	var errs []string
	cfg := p.Config
	events := map[string]Event{}
	for _, e := range p.Events {
		events[e.ID] = e
	}
	rooms := map[string]Room{}
	for _, r := range p.Rooms {
		rooms[r.ID] = r
	}
	unavail := map[string]map[int]bool{}
	for _, i := range p.Instructors {
		unavail[i.ID] = map[int]bool{}
		for _, t := range i.Unavailable {
			unavail[i.ID][t] = true
		}
	}

	placed := map[string]Placement{}
	for _, pl := range placements {
		if _, dup := placed[pl.EventID]; dup {
			errs = append(errs, fmt.Sprintf("%s birden fazla yerleştirilmiş", pl.EventID))
		}
		placed[pl.EventID] = pl
	}
	for _, e := range p.Events {
		pl, ok := placed[e.ID]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s yerleştirilmemiş", e.ID))
			continue
		}
		if pl.Day < 0 || pl.Day >= cfg.Days || pl.Slot < 0 || pl.Slot+e.Duration > cfg.SlotsPerDay || pl.Duration != e.Duration {
			errs = append(errs, fmt.Sprintf("%s: gün/saat/süre geçersiz", e.ID))
			continue
		}
		room, ok := rooms[pl.RoomID]
		if !ok || !roomFits(e, room) {
			errs = append(errs, fmt.Sprintf("%s: %s dersliği uygun değil", e.ID, pl.RoomID))
		}
		for h := 0; h < e.Duration; h++ {
			if e.Instructor != "" && unavail[e.Instructor][pl.start(cfg)+h] {
				errs = append(errs, fmt.Sprintf("%s: öğretim üyesi %s müsait değil", e.ID, e.Instructor))
				break
			}
		}
	}

	// Çiftler halinde çakışma denetimi.
	ids := make([]string, 0, len(placed))
	for id := range placed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := placed[ids[i]], placed[ids[j]]
			ea, eb := events[a.EventID], events[b.EventID]
			if a.start(cfg) >= b.start(cfg)+b.Duration || b.start(cfg) >= a.start(cfg)+a.Duration {
				continue // zaman olarak ayrık
			}
			if a.RoomID == b.RoomID {
				errs = append(errs, fmt.Sprintf("%s ve %s aynı derslikte çakışıyor", a.EventID, b.EventID))
			}
			if ea.Instructor != "" && ea.Instructor == eb.Instructor {
				errs = append(errs, fmt.Sprintf("%s ve %s aynı öğretim üyesinde çakışıyor", a.EventID, b.EventID))
			}
			for _, g := range ea.Groups {
				if slices.Contains(eb.Groups, g) {
					errs = append(errs, fmt.Sprintf("%s ve %s aynı grupta (%s) çakışıyor", a.EventID, b.EventID, g))
					break
				}
			}
		}
	}
	return errs
}
