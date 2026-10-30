package rules

import (
	"fmt"
	"slices"
	"sort"
)

// İhlal kodları (API sözleşmesinin parçası).
const (
	CodePrereqNotMet    = "PREREQ_NOT_MET"
	CodeTimeConflict    = "TIME_CONFLICT"
	CodeDuplicateCourse = "DUPLICATE_COURSE"
	CodeAlreadyPassed   = "ALREADY_PASSED"
	CodeECTSLimit       = "ECTS_LIMIT_EXCEEDED"
)

type Meeting struct {
	Day      int
	StartMin int
	EndMin   int
}

type SectionInfo struct {
	SectionID int64
	CourseID  int64
	ECTS      int
	Meetings  []Meeting
}

// Input tek bir doğrulama için gereken tüm veriyi taşır; Check bunun dışında hiçbir şeye bakmaz.
type Input struct {
	Current     []SectionInfo // öğrencinin bu dönem zaten aldığı (aktif/bekleyen) şubeler
	Candidates  []SectionInfo // eklenmek istenen şubeler
	Passed      map[int64]bool
	GANO        float64
	Prereqs     map[int64]Node // yalnızca aday derslerin ön koşulları yeterli
	CourseCodes map[int64]string
}

type Violation struct {
	Code       string  `json:"code"`
	Message    string  `json:"message"`
	SectionIDs []int64 `json:"section_ids,omitempty"`
	CourseID   int64   `json:"course_id,omitempty"`
}

type Report struct {
	Violations []Violation `json:"violations"`
	TotalECTS  int         `json:"total_ects"`
	MaxECTS    int         `json:"max_ects"`
}

func (r Report) OK() bool { return len(r.Violations) == 0 }

// MaxECTS GANO'ya göre dönem AKTS üst limitini verir.
// GANO >= 3.00 → 40, >= 2.50 → 36, aksi halde 30. Geçmişi olmayan öğrenci (GANO 0) 30'dan başlar.
func MaxECTS(gano float64) int {
	switch {
	case gano >= 3.0:
		return 40
	case gano >= 2.5:
		return 36
	default:
		return 30
	}
}

// Check tüm kuralları değerlendirir ve ilk hatada DURMADAN bütün ihlalleri listeler.
// Çıktı deterministiktir: kural sırası sabit, kendi içinde şube/ders sırasına göredir.
func Check(in Input) Report {
	rep := Report{MaxECTS: MaxECTS(in.GANO), Violations: []Violation{}}
	code := func(id int64) string {
		if c, ok := in.CourseCodes[id]; ok {
			return c
		}
		return fmt.Sprintf("#%d", id)
	}

	// Aday ve mevcut şubeleri tek listede, şube kimliğine göre tekilleştirerek ele al.
	all := slices.Clone(in.Current)
	cand := make([]SectionInfo, 0, len(in.Candidates))
	for _, c := range in.Candidates {
		if !slices.ContainsFunc(all, func(s SectionInfo) bool { return s.SectionID == c.SectionID }) {
			all = append(all, c)
			cand = append(cand, c)
		}
	}
	sort.SliceStable(cand, func(i, j int) bool { return cand[i].SectionID < cand[j].SectionID })

	// 1) Ön koşullar ve daha önce geçilmiş dersler (yalnızca yeni adaylar için).
	for _, c := range cand {
		if in.Passed[c.CourseID] {
			rep.Violations = append(rep.Violations, Violation{
				Code: CodeAlreadyPassed, SectionIDs: []int64{c.SectionID}, CourseID: c.CourseID,
				Message: fmt.Sprintf("%s dersi daha önce başarıyla tamamlanmış", code(c.CourseID)),
			})
		}
		if expr, ok := in.Prereqs[c.CourseID]; ok && !expr.Eval(in.Passed) {
			rep.Violations = append(rep.Violations, Violation{
				Code: CodePrereqNotMet, SectionIDs: []int64{c.SectionID}, CourseID: c.CourseID,
				Message: fmt.Sprintf("%s için ön koşul sağlanmıyor: %s", code(c.CourseID), expr.Describe(in.CourseCodes)),
			})
		}
	}

	// 2) Aynı dersin birden fazla şubesi (mevcut + aday).
	byCourse := map[int64][]int64{}
	var courseOrder []int64
	for _, s := range all {
		if _, seen := byCourse[s.CourseID]; !seen {
			courseOrder = append(courseOrder, s.CourseID)
		}
		byCourse[s.CourseID] = append(byCourse[s.CourseID], s.SectionID)
	}
	slices.Sort(courseOrder)
	for _, cid := range courseOrder {
		if ids := byCourse[cid]; len(ids) > 1 {
			slices.Sort(ids)
			rep.Violations = append(rep.Violations, Violation{
				Code: CodeDuplicateCourse, SectionIDs: ids, CourseID: cid,
				Message: fmt.Sprintf("%s dersi birden fazla şubeye eklenemez", code(cid)),
			})
		}
	}

	// 3) Ders saati çakışmaları: her çift en fazla bir kez raporlanır.
	sorted := slices.Clone(all)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SectionID < sorted[j].SectionID })
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			a, b := sorted[i], sorted[j]
			// İkisi de mevcut ise sorun geçmişte oluşmuştur; yeni isteğin suçu değildir.
			if isIn(in.Current, a) && isIn(in.Current, b) {
				continue
			}
			if overlap(a.Meetings, b.Meetings) {
				rep.Violations = append(rep.Violations, Violation{
					Code: CodeTimeConflict, SectionIDs: []int64{a.SectionID, b.SectionID},
					Message: fmt.Sprintf("%s ve %s ders saatleri çakışıyor", code(a.CourseID), code(b.CourseID)),
				})
			}
		}
	}

	// 4) AKTS üst limiti.
	for _, s := range all {
		rep.TotalECTS += s.ECTS
	}
	if rep.TotalECTS > rep.MaxECTS {
		rep.Violations = append(rep.Violations, Violation{
			Code: CodeECTSLimit,
			Message: fmt.Sprintf("toplam %d AKTS, GANO %.2f için izin verilen üst sınır %d AKTS'yi aşıyor",
				rep.TotalECTS, in.GANO, rep.MaxECTS),
		})
	}
	return rep
}

func isIn(list []SectionInfo, s SectionInfo) bool {
	return slices.ContainsFunc(list, func(x SectionInfo) bool { return x.SectionID == s.SectionID })
}

// overlap iki şubenin herhangi bir oturumu aynı günde zaman olarak kesişiyorsa true.
// Yarı açık aralıklar kullanılır: 09:00-10:00 ile 10:00-11:00 çakışmaz.
func overlap(a, b []Meeting) bool {
	for _, x := range a {
		for _, y := range b {
			if x.Day == y.Day && x.StartMin < y.EndMin && y.StartMin < x.EndMin {
				return true
			}
		}
	}
	return false
}
