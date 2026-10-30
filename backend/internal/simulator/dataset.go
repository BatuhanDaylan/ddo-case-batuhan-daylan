// Package simulator büyük ölçekli, tohumlu (deterministik) test verisi üretir ve "kayıt anı" yükünü taklit eder.
package simulator

import (
	"fmt"
	"math/rand/v2"
)

// Scale veri hacmidir.
type Scale struct {
	Students int
	Courses  int // toplam ders (son Storm tanesi "kayıt anı" dersleridir)
	Sections int // toplam şube (Storm tanesi kayıt anı şubesidir)
	Storm    int // kontenjanı 40 olan, kayıt anı yarışı için ayrılmış şube sayısı
}

// Full şartnamedeki ölçektir.
var Full = Scale{Students: 12000, Courses: 600, Sections: 1500, Storm: 30}

const (
	idBase       = 1_000_000 // simülatör kimlikleri demo verisiyle çakışmasın
	programs     = 24
	departments  = 12
	faculties    = 4
	instructors  = 120
	advisors     = 200
	firstYear    = 2022 // 4 yıllık geçmişin ilk giriş yılı
	lastYear     = 2025 // son tamamlanan öğretim yılı
	StudentEmail = "sim%05d@sim.test"
)

// Table COPY ile yüklenmeye hazır bir tablodur.
type Table struct {
	Name string
	Cols []string
	Rows [][]any
}

// Dataset üretilen tüm veridir; Tables yükleme sırasıyla (yabancı anahtarlara uygun) dizilidir.
type Dataset struct {
	Tables []Table
	Scale  Scale
}

func (d Dataset) Table(name string) *Table {
	for i := range d.Tables {
		if d.Tables[i].Name == name {
			return &d.Tables[i]
		}
	}
	return nil
}

var givenNames = []string{"Ayşe", "Mehmet", "Fatma", "Ali", "Zeynep", "Mustafa", "Elif", "Ahmet", "Hatice", "Hasan", "Emine", "Hüseyin", "Merve", "İbrahim", "Büşra", "Yusuf", "Selin", "Burak", "Esra", "Emre"}
var surnames = []string{"Yılmaz", "Kaya", "Demir", "Şahin", "Çelik", "Yıldız", "Yıldırım", "Öztürk", "Aydın", "Özdemir", "Arslan", "Doğan", "Kılıç", "Aslan", "Çetin", "Kara", "Koç", "Kurt", "Özkan", "Şimşek"}

// Harf notu dağılımı (yüzde): AA 10, BA 15, BB 20, CB 20, CC 15, DC 8, DD 5, FF 7.
var letterDist = []struct {
	letter string
	point  float64
	weight int
	passed bool
}{
	{"AA", 4.0, 10, true}, {"BA", 3.5, 15, true}, {"BB", 3.0, 20, true}, {"CB", 2.5, 20, true},
	{"CC", 2.0, 15, true}, {"DC", 1.5, 8, true}, {"DD", 1.0, 5, true}, {"FF", 0.0, 7, false},
}

func pickLetter(rng *rand.Rand) (string, float64, bool) {
	n := rng.IntN(100)
	for _, l := range letterDist {
		if n < l.weight {
			return l.letter, l.point, l.passed
		}
		n -= l.weight
	}
	return "FF", 0, false
}

// Generate verilen tohumla veriyi üretir. Aynı (seed, scale, passwordHash) her zaman aynı veriyi verir.
// passwordHash, tüm simülasyon kullanıcılarının ortak parola özetidir (12.000 argon2 hesabı yerine tek hesap).
func Generate(seed uint64, sc Scale, passwordHash string) Dataset {
	rng := rand.New(rand.NewPCG(seed, 0xC0FFEE))
	next := map[string]int64{}
	id := func(table string) int64 {
		next[table]++
		return idBase + next[table]
	}

	var (
		tFaculties = Table{Name: "faculties", Cols: []string{"id", "code", "name"}}
		tDepts     = Table{Name: "departments", Cols: []string{"id", "faculty_id", "code", "name"}}
		tPrograms  = Table{Name: "programs", Cols: []string{"id", "department_id", "code", "name", "total_ects_required"}}
		tCourses   = Table{Name: "courses", Cols: []string{"id", "code", "name", "ects", "department_id", "weekly_hours", "room_kind"}}
		tTerms     = Table{Name: "terms", Cols: []string{"id", "code", "name", "registration_open"}}
		tUsers     = Table{Name: "users", Cols: []string{"id", "email", "password_hash", "full_name", "role"}}
		tStudents  = Table{Name: "students", Cols: []string{"id", "user_id", "student_no", "program_id", "entry_year", "advisor_id"}}
		tVersions  = Table{Name: "curriculum_versions", Cols: []string{"id", "program_id", "effective_year"}}
		tCurr      = Table{Name: "curriculum_courses", Cols: []string{"curriculum_version_id", "course_id", "semester", "kind"}}
		tSections  = Table{Name: "sections", Cols: []string{"id", "term_id", "course_id", "code", "instructor_id", "capacity", "enrolled_count"}}
		tResults   = Table{Name: "course_results", Cols: []string{"id", "student_id", "course_id", "term_id", "letter", "grade_point", "passed"}}
	)

	// Akademik yapı.
	facIDs := make([]int64, faculties)
	for i := range facIDs {
		facIDs[i] = id("faculties")
		tFaculties.Rows = append(tFaculties.Rows, []any{facIDs[i], fmt.Sprintf("SIM-F%d", i+1), fmt.Sprintf("Simülasyon Fakültesi %d", i+1)})
	}
	deptIDs := make([]int64, departments)
	for i := range deptIDs {
		deptIDs[i] = id("departments")
		tDepts.Rows = append(tDepts.Rows, []any{deptIDs[i], facIDs[i%faculties], fmt.Sprintf("SIM-D%02d", i+1), fmt.Sprintf("Simülasyon Bölümü %d", i+1)})
	}
	progIDs := make([]int64, programs)
	progDept := make([]int, programs)
	for i := range progIDs {
		progIDs[i] = id("programs")
		progDept[i] = i % departments
		tPrograms.Rows = append(tPrograms.Rows, []any{progIDs[i], deptIDs[progDept[i]], fmt.Sprintf("SIM-P%02d", i+1), fmt.Sprintf("Simülasyon Programı %d", i+1), 240})
	}

	// Dersler: ilk Courses-Storm normal, son Storm tane kayıt anı dersi (3 AKTS, tek şube, kontenjan 40).
	normal := sc.Courses - sc.Storm
	courseIDs := make([]int64, sc.Courses)
	courseDept := make([]int, sc.Courses)
	ectsChoices := []int{3, 4, 5, 6, 6, 8}
	for i := 0; i < sc.Courses; i++ {
		courseIDs[i] = id("courses")
		courseDept[i] = i % departments
		code, name, ects := fmt.Sprintf("SIM-C%03d", i+1), fmt.Sprintf("Simülasyon Dersi %d", i+1), ectsChoices[rng.IntN(len(ectsChoices))]
		if i >= normal {
			code, name, ects = fmt.Sprintf("SIM-STORM-%02d", i-normal+1), fmt.Sprintf("Kayıt Anı Dersi %d", i-normal+1), 3
		}
		tCourses.Rows = append(tCourses.Rows, []any{courseIDs[i], code, name, ects, deptIDs[courseDept[i]], 2 + rng.IntN(3), "lecture"})
	}

	// Dönemler: 8 geçmiş dönem (kayıt kapalı) + açık güncel dönem.
	var pastTerms []int64
	for y := firstYear; y <= lastYear; y++ {
		for _, name := range []string{"GUZ", "BAHAR"} {
			tid := id("terms")
			pastTerms = append(pastTerms, tid)
			tTerms.Rows = append(tTerms.Rows, []any{tid, fmt.Sprintf("SIM-%d-%s", y, name), fmt.Sprintf("%d %s (simülasyon)", y, name), false})
		}
	}
	currentTerm := id("terms")
	tTerms.Rows = append(tTerms.Rows, []any{currentTerm, fmt.Sprintf("SIM-%d-GUZ", lastYear+1), fmt.Sprintf("%d Güz (simülasyon)", lastYear+1), true})

	// Müfredatlar: her program için 2 sürüm (2020, 2024); 8 yarıyıl × 5 zorunlu + 6 seçmeli ders.
	type curr struct{ bySemester [9][]int64 }
	curricula := map[[2]int]*curr{} // [program, sürüm indeksi]
	for pi := range progIDs {
		var deptCourses []int
		for ci := 0; ci < normal; ci++ {
			if courseDept[ci] == progDept[pi] {
				deptCourses = append(deptCourses, ci)
			}
		}
		for vi, year := range []int{2020, 2024} {
			vid := id("curriculum_versions")
			tVersions.Rows = append(tVersions.Rows, []any{vid, progIDs[pi], year})
			shuffled := append([]int(nil), deptCourses...)
			rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			c := &curr{}
			for k := 0; k < 40 && k < len(shuffled); k++ {
				sem := k/5 + 1
				c.bySemester[sem] = append(c.bySemester[sem], courseIDs[shuffled[k]])
				tCurr.Rows = append(tCurr.Rows, []any{vid, courseIDs[shuffled[k]], sem, "required"})
			}
			for k := 40; k < 46 && k < len(shuffled); k++ {
				tCurr.Rows = append(tCurr.Rows, []any{vid, courseIDs[shuffled[k]], 5 + (k-40)%4, "elective"})
			}
			curricula[[2]int{pi, vi}] = c
		}
	}

	// Kullanıcılar: öğretim üyeleri, danışmanlar, öğrenciler (ortak parola özeti).
	user := func(email, name, role string) int64 {
		uid := id("users")
		tUsers.Rows = append(tUsers.Rows, []any{uid, email, passwordHash, name, role})
		return uid
	}
	fullName := func() string { return givenNames[rng.IntN(len(givenNames))] + " " + surnames[rng.IntN(len(surnames))] }
	instrIDs := make([]int64, instructors)
	for i := range instrIDs {
		instrIDs[i] = user(fmt.Sprintf("simhoca%03d@sim.test", i+1), fullName(), "instructor")
	}
	advIDs := make([]int64, advisors)
	for i := range advIDs {
		advIDs[i] = user(fmt.Sprintf("simdanisman%03d@sim.test", i+1), fullName(), "advisor")
	}

	// Şubeler: normal derslere eşit dağıtılmış, kayıt anı derslerine kontenjanı 40 olan tek şube.
	perCourse, extra := (sc.Sections-sc.Storm)/normal, (sc.Sections-sc.Storm)%normal
	n := 0
	for ci := 0; ci < normal; ci++ {
		count := perCourse
		if ci < extra {
			count++
		}
		for k := 0; k < count; k++ {
			tSections.Rows = append(tSections.Rows, []any{id("sections"), currentTerm, courseIDs[ci], fmt.Sprintf("%02d", k+1),
				instrIDs[n%instructors], 30 + rng.IntN(91), 0})
			n++
		}
	}
	for ci := normal; ci < sc.Courses; ci++ {
		tSections.Rows = append(tSections.Rows, []any{id("sections"), currentTerm, courseIDs[ci], "01", instrIDs[n%instructors], 40, 0})
		n++
	}

	// Öğrenciler ve 4 yıllık not geçmişi: girişten son tamamlanan döneme kadar her dönem 5 zorunlu ders.
	for i := 0; i < sc.Students; i++ {
		entry := firstYear + i%4
		pi := rng.IntN(programs)
		vi := 0
		if entry >= 2024 {
			vi = 1
		}
		uid := user(fmt.Sprintf(StudentEmail, i+1), fullName(), "student")
		sid := id("students")
		tStudents.Rows = append(tStudents.Rows, []any{sid, uid, fmt.Sprintf("SIM%06d", i+1), progIDs[pi], entry, advIDs[rng.IntN(advisors)]})

		c := curricula[[2]int{pi, vi}]
		startTerm := (entry - firstYear) * 2
		for sem := 1; startTerm+sem-1 < len(pastTerms) && sem <= 8; sem++ {
			for _, cid := range c.bySemester[sem] {
				letter, point, passed := pickLetter(rng)
				tResults.Rows = append(tResults.Rows, []any{id("course_results"), sid, cid, pastTerms[startTerm+sem-1], letter, point, passed})
			}
		}
	}

	return Dataset{
		Scale: sc,
		Tables: []Table{tFaculties, tDepts, tPrograms, tCourses, tTerms, tUsers, tStudents,
			tVersions, tCurr, tSections, tResults},
	}
}
