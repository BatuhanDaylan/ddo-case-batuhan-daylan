package scheduler

import (
	"fmt"
	"sort"
)

// analyze aramaya gerek kalmadan çözümsüzlüğü KANITLAYAN nedenleri bulur ve adlandırır.
// Boş dönerse bu analiz çözümsüzlük kanıtlayamadı demektir (çözüm olabilir ya da arama gerekir).
//
// Kontroller (hepsi gerekli koşuldur: ihlal varsa çözüm yoktur):
//  1. Etkinlik için kapasite+türe uyan hiç derslik yok.
//  2. Öğretim üyesinin, etkinlik süresi kadar ardışık müsait saati yok.
//  3. Öğretim üyesinin toplam ders saati, müsait saatlerinden fazla.
//  4. Bir grubun toplam ders saati, haftalık toplam saatten fazla (grup içi çakışma yasak olduğu için).
//  5. Derslik türü/kapasite havuzlarının toplam talebi, havuzun haftalık kapasitesinden fazla.
func analyze(p Problem, s *solver) []Conflict {
	var out []Conflict
	cfg := p.Config
	total := cfg.Slots()

	for ei, e := range p.Events {
		if len(s.compat[ei]) == 0 {
			out = append(out, Conflict{
				Kind: ConflictNoRoom, Events: []string{e.ID},
				Message: fmt.Sprintf("%s (%s): kapasitesi ≥ %d olan %s türünde derslik yok", e.ID, e.Name, e.Students, kindName(e.Kind)),
			})
		}
		if len(s.starts[ei]) == 0 {
			out = append(out, Conflict{
				Kind: ConflictInstructorAvail, Events: []string{e.ID},
				Message: fmt.Sprintf("%s (%s): öğretim üyesi %s'nin %d saat ardışık müsait aralığı yok", e.ID, e.Name, e.Instructor, e.Duration),
			})
		}
	}

	// Öğretim üyesi yükü.
	byInstr := map[string][]string{}
	hoursByInstr := map[string]int{}
	for _, e := range p.Events {
		if e.Instructor != "" {
			byInstr[e.Instructor] = append(byInstr[e.Instructor], e.ID)
			hoursByInstr[e.Instructor] += e.Duration
		}
	}
	for _, in := range p.Instructors {
		avail := total - len(uniqueInts(in.Unavailable))
		if need := hoursByInstr[in.ID]; need > avail {
			out = append(out, Conflict{
				Kind: ConflictInstructorOverload, Events: byInstr[in.ID],
				Message: fmt.Sprintf("Öğretim üyesi %s: toplam %d saat ders var ama haftada yalnızca %d saat müsait", in.ID, need, avail),
			})
		}
	}

	// Grup yükü.
	byGroup := map[string][]string{}
	hoursByGroup := map[string]int{}
	for _, e := range p.Events {
		for _, g := range e.Groups {
			byGroup[g] = append(byGroup[g], e.ID)
			hoursByGroup[g] += e.Duration
		}
	}
	groupNames := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	for _, g := range groupNames {
		if need := hoursByGroup[g]; need > total {
			out = append(out, Conflict{
				Kind: ConflictGroupOverload, Events: byGroup[g],
				Message: fmt.Sprintf("Grup %s: aynı anda alınamayan dersler toplam %d saat; haftada en fazla %d saat var", g, need, total),
			})
		}
	}

	// Derslik havuzu yükü: her derslik haftada Slots() saat sunar.
	// Lab etkinlikleri yalnızca lab'lara, amfi etkinlikleri yalnızca amfilere, sınıf etkinlikleri sınıf+amfilere girer.
	count := func(kinds ...string) int {
		n := 0
		for _, r := range p.Rooms {
			for _, k := range kinds {
				if r.Kind == k {
					n++
				}
			}
		}
		return n
	}
	need := map[string]int{}
	ids := map[string][]string{}
	for _, e := range p.Events {
		need[e.Kind] += e.Duration
		ids[e.Kind] = append(ids[e.Kind], e.ID)
	}
	checks := []struct {
		name  string
		hours int
		cap   int
		ev    []string
	}{
		{"lab", need[KindLab], count(KindLab) * total, ids[KindLab]},
		{"amfi", need[KindAmphi], count(KindAmphi) * total, ids[KindAmphi]},
		{"sınıf+amfi (sınıf türü dersler)", need[KindLecture] + need[KindAmphi], count(KindLecture, KindAmphi) * total,
			append(append([]string{}, ids[KindLecture]...), ids[KindAmphi]...)},
		{"tüm derslikler", need[KindLecture] + need[KindAmphi] + need[KindLab], len(p.Rooms) * total,
			append(append(append([]string{}, ids[KindLecture]...), ids[KindAmphi]...), ids[KindLab]...)},
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if c.hours > c.cap && !seen[c.name] {
			seen[c.name] = true
			out = append(out, Conflict{
				Kind: ConflictRoomCapacity, Events: c.ev,
				Message: fmt.Sprintf("Derslik kapasitesi yetersiz (%s): %d saat talep var, haftalık toplam %d saat sunuluyor", c.name, c.hours, c.cap),
			})
		}
	}
	return out
}

func kindName(k string) string {
	switch k {
	case KindLab:
		return "laboratuvar"
	case KindAmphi:
		return "amfi"
	default:
		return "sınıf/amfi"
	}
}

func uniqueInts(xs []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
