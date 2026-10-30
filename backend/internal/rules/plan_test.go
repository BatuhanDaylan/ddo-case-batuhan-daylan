package rules

import "testing"

func TestExpandPassed(t *testing.T) {
	eq := Equivalences{10: {20, 21}, 30: {10}} // 10 yerine 20 veya 21; 30 yerine 10
	in := set(20)
	out := ExpandPassed(in, eq)
	if !out[10] || !out[20] {
		t.Errorf("denklik uygulanmadı: %v", out)
	}
	if out[30] {
		t.Error("denklik zincirleme uygulanmamalı (30 yerine 10, ama 10 doğrudan geçilmedi)")
	}
	if in[10] {
		t.Error("girdi kümesi değiştirildi")
	}
	if got := ExpandPassed(set(), eq); len(got) != 0 {
		t.Errorf("boş küme genişledi: %v", got)
	}
	if got := ExpandPassed(map[int64]bool{5: false}, nil); len(got) != 0 {
		t.Errorf("false değerli giriş geçilmiş sayıldı: %v", got)
	}
}

func TestCourseStatus(t *testing.T) {
	eq := Equivalences{1: {3, 2}}
	cases := []struct {
		name           string
		passed, exempt map[int64]bool
		course         int64
		want           string
		via            int64
	}{
		{"bekliyor", set(), set(), 1, StatusPending, 0},
		{"doğrudan", set(1), set(), 1, StatusPassed, 0},
		{"muaf", set(1), set(1), 1, StatusExempt, 0},
		{"denklik (en küçük kimlik seçilir)", set(2, 3), set(), 1, StatusEquivalent, 2},
		{"denklik muafiyetle", set(), set(3), 1, StatusEquivalent, 3},
		{"ilgisiz ders", set(9), set(), 1, StatusPending, 0},
	}
	for _, c := range cases {
		got, via := CourseStatus(c.course, c.passed, c.exempt, eq)
		if got != c.want || via != c.via {
			t.Errorf("%s: (%s,%d), beklenen (%s,%d)", c.name, got, via, c.want, c.via)
		}
	}
}

func TestStudyYearAndDue(t *testing.T) {
	// 2021 girişli öğrenci, 2024'te 4. yılında.
	if got := StudyYear(2021, 2024); got != 4 {
		t.Errorf("StudyYear = %d, beklenen 4", got)
	}
	if got := StudyYear(2021, 2021); got != 1 {
		t.Errorf("giriş yılı = %d, beklenen 1", got)
	}
	if got := StudyYear(2021, 2019); got != 0 {
		t.Errorf("giriş öncesi = %d, beklenen 0", got)
	}
	cases := []struct {
		sem, year int
		want      string
	}{
		{1, 1, "current"}, {2, 1, "current"}, {3, 1, "future"},
		{6, 4, "overdue"}, {7, 4, "current"}, {8, 4, "current"}, {9, 4, "future"},
		{1, 0, "future"},
	}
	for _, c := range cases {
		if got := Due(c.sem, c.year); got != c.want {
			t.Errorf("Due(%d,%d) = %s, beklenen %s", c.sem, c.year, got, c.want)
		}
	}
}
