package rules

import "slices"

// Equivalences: gereksinim dersi → onun yerine sayılabilecek dersler ("X yerine Y sayılır").
type Equivalences map[int64][]int64

// ExpandPassed, denkliklere göre "geçilmiş sayılan" dersler kümesini genişletir.
// Girdi kümesini değiştirmez. Tek seviyelidir: yeni eklenen dersler tekrar genişletmeye girmez.
func ExpandPassed(passed map[int64]bool, eq Equivalences) map[int64]bool {
	out := make(map[int64]bool, len(passed))
	for id, ok := range passed {
		if ok {
			out[id] = true
		}
	}
	for course, subs := range eq {
		if out[course] {
			continue
		}
		for _, s := range subs {
			if passed[s] {
				out[course] = true
				break
			}
		}
	}
	return out
}

// Müfredat dersi durumları.
const (
	StatusExempt     = "exempt"
	StatusPassed     = "passed"
	StatusEquivalent = "equivalent"
	StatusPending    = "pending"
)

// CourseStatus bir müfredat dersinin öğrenci için durumunu ve (denklik ise) hangi dersle
// karşılandığını verir. Öncelik: muafiyet > doğrudan geçme > denklik > bekliyor.
func CourseStatus(courseID int64, passed, exempt map[int64]bool, eq Equivalences) (status string, via int64) {
	switch {
	case exempt[courseID]:
		return StatusExempt, 0
	case passed[courseID]:
		return StatusPassed, 0
	}
	subs := slices.Clone(eq[courseID])
	slices.Sort(subs) // deterministik sonuç
	for _, s := range subs {
		if passed[s] || exempt[s] {
			return StatusEquivalent, s
		}
	}
	return StatusPending, 0
}

// StudyYear, giriş yılı entryYear olan öğrencinin asOf yılında kaçıncı öğretim yılında olduğunu verir
// (giriş yılı = 1. yıl). Giriş yılından önceki tarihler için 0 döner.
func StudyYear(entryYear, asOf int) int {
	if asOf < entryYear {
		return 0
	}
	return asOf - entryYear + 1
}

// Due, bir yarıyılın asOf tarihinde öğrenci için ne durumda olduğunu söyler:
//   - "overdue": önceki öğretim yıllarına ait, asOf'tan önce tamamlanmış olması gereken yarıyıl
//   - "current": asOf yılındaki (güz+bahar) yarıyıllar
//   - "future" : henüz sırası gelmemiş
func Due(semester, studyYear int) string {
	switch {
	case semester <= 2*(studyYear-1):
		return "overdue"
	case semester <= 2*studyYear:
		return "current"
	default:
		return "future"
	}
}
