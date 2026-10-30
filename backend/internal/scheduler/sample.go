package scheduler

import (
	"fmt"
	"math/rand/v2"
)

// Sample şartnamedeki ölçekte (60 ders / 12 derslik / 20 öğretim üyesi) deterministik bir örnek
// problem üretir: 6 yarıyıl grubu × 8 zorunlu ders + 12 seçmeli; 6 sınıf, 3 laboratuvar, 3 amfi.
// Aynı tohum her zaman aynı problemi verir.
func Sample(seed int64) Problem {
	rng := rand.New(rand.NewPCG(uint64(seed), 0x5eed))
	p := Problem{Config: Config{Days: 5, SlotsPerDay: 8}}

	for i, c := range []int{30, 35, 40, 45, 50, 60} {
		p.Rooms = append(p.Rooms, Room{ID: fmt.Sprintf("SINIF-%d", i+1), Capacity: c, Kind: KindLecture})
	}
	for i, c := range []int{20, 24, 30} {
		p.Rooms = append(p.Rooms, Room{ID: fmt.Sprintf("LAB-%d", i+1), Capacity: c, Kind: KindLab})
	}
	for i, c := range []int{90, 120, 150} {
		p.Rooms = append(p.Rooms, Room{ID: fmt.Sprintf("AMFI-%d", i+1), Capacity: c, Kind: KindAmphi})
	}

	for i := 1; i <= 20; i++ {
		in := Instructor{ID: fmt.Sprintf("OU-%02d", i)}
		for k := 0; k < 4+rng.IntN(5); k++ { // haftada 4-8 saat müsait değil
			in.Unavailable = append(in.Unavailable, rng.IntN(p.Config.Slots()))
		}
		p.Instructors = append(p.Instructors, in)
	}

	for i := 0; i < 60; i++ {
		e := Event{
			ID:         fmt.Sprintf("D%02d", i+1),
			Name:       fmt.Sprintf("Ders %d", i+1),
			Instructor: p.Instructors[i%20].ID, // her öğretim üyesine 3 ders
			Duration:   2,
		}
		if rng.IntN(4) == 0 {
			e.Duration = 3
		}
		switch r := rng.IntN(100); {
		case r < 70:
			e.Kind, e.Students = KindLecture, 20+rng.IntN(36)
		case r < 85:
			e.Kind, e.Students = KindLab, 15+rng.IntN(10)
		default:
			e.Kind, e.Students = KindAmphi, 80+rng.IntN(60)
		}
		if i < 48 { // ilk 48 ders 6 yarıyılın zorunlu dersleri; kalan 12 seçmeli
			e.Groups = []string{fmt.Sprintf("YARIYIL-%d", i/8+1)}
		}
		p.Events = append(p.Events, e)
	}
	return p
}
