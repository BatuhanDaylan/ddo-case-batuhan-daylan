package grading

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestAbsoluteLetterBoundaries(t *testing.T) {
	cases := map[float64]string{
		100: "AA", 90: "AA", 89.99: "BA", 85: "BA", 84.99: "BB", 80: "BB", 79.99: "CB", 75: "CB",
		74.99: "CC", 65: "CC", 64.99: "DC", 58: "DC", 57.99: "DD", 50: "DD", 49.99: "FF", 0: "FF",
	}
	for total, want := range cases {
		if got := AbsoluteLetter(total); got != want {
			t.Errorf("AbsoluteLetter(%v) = %s, beklenen %s", total, got, want)
		}
	}
}

func TestPointOfAndPassed(t *testing.T) {
	if p, ok := PointOf("BA"); !ok || p != 3.5 {
		t.Errorf("BA = (%v,%v)", p, ok)
	}
	if _, ok := PointOf("ZZ"); ok {
		t.Error("geçersiz harf kabul edildi")
	}
	for l, want := range map[string]bool{"AA": true, "DD": true, "FF": false, "ZZ": false} {
		if Passed(l) != want {
			t.Errorf("Passed(%s) != %v", l, want)
		}
	}
}

func TestEffectivePriority(t *testing.T) {
	got := Effective(map[string]map[string]float64{
		Final:    {KindRegular: 30, KindMakeup: 70},
		Midterm:  {KindRegular: 40, KindExcuse: 60},
		Homework: {KindRegular: 80},
	})
	if got[Final] != 70 || got[Midterm] != 60 || got[Homework] != 80 {
		t.Errorf("beklenmeyen geçerli puanlar: %v", got)
	}
	// Hem bütünleme hem mazeret varsa bütünleme kazanır.
	both := Effective(map[string]map[string]float64{Final: {KindExcuse: 55, KindMakeup: 65}})
	if both[Final] != 65 {
		t.Errorf("bütünleme önceliği: %v", both)
	}
}

func TestTotal(t *testing.T) {
	w := map[string]int{Midterm: 30, Final: 50, Homework: 20}
	if got := Total(w, map[string]float64{Midterm: 80, Final: 70, Homework: 100}); got != 79 {
		t.Errorf("Total = %v, beklenen 79", got)
	}
	// Girilmemiş bileşen 0 sayılır.
	if got := Total(w, map[string]float64{Midterm: 100}); got != 30 {
		t.Errorf("eksik bileşenli Total = %v, beklenen 30", got)
	}
}

func TestMeanStdAndTScore(t *testing.T) {
	m, sd := MeanStd([]float64{2, 4, 4, 4, 5, 5, 7, 9}) // klasik örnek: ort 5, sapma 2
	if m != 5 || sd != 2 {
		t.Errorf("MeanStd = (%v,%v), beklenen (5,2)", m, sd)
	}
	if got := TScore(7, m, sd); got != 60 {
		t.Errorf("TScore = %v, beklenen 60", got)
	}
	if got := TScore(5, 5, 0); got != 50 {
		t.Errorf("sapma 0: %v", got)
	}
	if m, sd := MeanStd(nil); m != 0 || sd != 0 {
		t.Errorf("boş: (%v,%v)", m, sd)
	}
}

func TestRelativeLetters(t *testing.T) {
	// Ortalama 60,5 ve sapma ~14,6: 95 → T≈73,7 (AA); 60 → T≈49,7 (CC); 30 taban altı (FF).
	totals := []float64{95, 60, 60, 60, 60, 60, 60, 60, 60, 30}
	got := RelativeLetters(totals)
	if got[0] != "AA" || got[1] != "CC" || got[9] != "FF" {
		t.Errorf("beklenen AA/CC/FF, gelen %s/%s/%s (%v)", got[0], got[1], got[9], got)
	}
	// Uç değer sapmayı şişirir: aynı 95, geniş dağılımlı sınıfta AA'ya yetişemez (T≈63,9 → BA).
	spread := RelativeLetters([]float64{95, 85, 78, 70, 70, 62, 55, 45, 30, 90})
	if spread[0] != "BA" {
		t.Errorf("geniş dağılımda en yüksek %s, beklenen BA (%v)", spread[0], spread)
	}
	// Herkes aynı puanı aldıysa sapma 0 → T=50 → CB (taban üstündeyse).
	for _, l := range RelativeLetters([]float64{70, 70, 70}) {
		if l != "CB" {
			t.Errorf("eşit puanlı sınıfta %s, beklenen CB", l)
		}
	}
	// Sınıf ortalaması düşük olsa da taban uygulanır.
	for _, l := range RelativeLetters([]float64{30, 30, 30}) {
		if l != "FF" {
			t.Errorf("tabanın altında %s, beklenen FF", l)
		}
	}
}

// Özellik: aynı sınıf içinde yüksek puan hiçbir zaman daha kötü harf almaz (tekdüzelik),
// hem mutlak hem bağıl yöntemde.
func TestLettersMonotonicProperty(t *testing.T) {
	rank := func(l string) int {
		for i, s := range Scale {
			if s.Name == l {
				return i // küçük = iyi
			}
		}
		return -1
	}
	rng := rand.New(rand.NewPCG(7, 8))
	for iter := 0; iter < 300; iter++ {
		n := 2 + rng.IntN(40)
		totals := make([]float64, n)
		for i := range totals {
			totals[i] = math.Round(rng.Float64()*10000) / 100
		}
		for _, method := range []string{MethodAbsolute, MethodRelative} {
			letters := Letters(method, totals)
			for i := range totals {
				for j := range totals {
					if totals[i] > totals[j] && rank(letters[i]) > rank(letters[j]) {
						t.Fatalf("%s: %.2f→%s, %.2f→%s (yüksek puan daha kötü not aldı)",
							method, totals[i], letters[i], totals[j], letters[j])
					}
				}
			}
		}
	}
}

func TestGanoPolicies(t *testing.T) {
	// Ders 1: önce FF (0.0), sonra BB (3.0). Ders 2: AA (4.0).
	attempts := []Attempt{
		{CourseID: 1, TermOrder: 1, ECTS: 6, Point: 0},
		{CourseID: 1, TermOrder: 2, ECTS: 6, Point: 3.0},
		{CourseID: 2, TermOrder: 1, ECTS: 4, Point: 4.0},
	}
	if got := Gano(attempts, PolicyLast); got != 3.4 { // (3*6 + 4*4)/10
		t.Errorf("last = %v, beklenen 3.4", got)
	}
	// Son deneme daha kötüyse: en son politikası kötü notu, en yüksek politikası iyisini alır.
	worse := []Attempt{
		{CourseID: 1, TermOrder: 1, ECTS: 6, Point: 3.0},
		{CourseID: 1, TermOrder: 2, ECTS: 6, Point: 1.0},
	}
	if got := Gano(worse, PolicyLast); got != 1 {
		t.Errorf("last/kötüleşen = %v, beklenen 1", got)
	}
	if got := Gano(worse, PolicyBest); got != 3 {
		t.Errorf("best/kötüleşen = %v, beklenen 3", got)
	}
	// Girdi sırası sonucu değiştirmemeli.
	rev := slices.Clone(attempts)
	slices.Reverse(rev)
	if Gano(rev, PolicyLast) != Gano(attempts, PolicyLast) {
		t.Error("deneme sırası sonucu değiştirdi")
	}
}

func TestGanoExemptAndEmpty(t *testing.T) {
	if got := Gano(nil, PolicyLast); got != 0 {
		t.Errorf("boş = %v", got)
	}
	got := Gano([]Attempt{
		{CourseID: 1, TermOrder: 1, ECTS: 5, Point: 0, Exempt: true},
		{CourseID: 2, TermOrder: 1, ECTS: 5, Point: 2.0},
	}, PolicyLast)
	if got != 2 {
		t.Errorf("muaf ders GANO'ya girdi: %v", got)
	}
	if got := Gano([]Attempt{{CourseID: 1, ECTS: 5, Exempt: true}}, PolicyLast); got != 0 {
		t.Errorf("yalnızca muaf = %v, beklenen 0", got)
	}
}

// Özellik: GANO her zaman [0,4] aralığındadır ve 'best' hiçbir zaman 'last'ten düşük olamaz.
func TestGanoBoundsAndBestDominatesProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for iter := 0; iter < 300; iter++ {
		var at []Attempt
		for i := 0; i < 1+rng.IntN(20); i++ {
			course := int64(1 + rng.IntN(6))
			at = append(at, Attempt{
				CourseID: course, TermOrder: int64(rng.IntN(5)),
				ECTS:  2 + int(course), // bir dersin AKTS'si denemeden denemeye değişmez
				Point: float64(rng.IntN(9)) / 2,
			})
		}
		last, best := Gano(at, PolicyLast), Gano(at, PolicyBest)
		if last < 0 || last > 4 || best < 0 || best > 4 {
			t.Fatalf("aralık dışı: last=%v best=%v", last, best)
		}
		// Ders başına seçilen en iyi puan >= seçilen son puan, ama AKTS ağırlıkları aynı kümede
		// olduğu için ortalama da en az o kadar olmalı.
		if best+1e-9 < last {
			t.Fatalf("best (%v) < last (%v)", best, last)
		}
	}
}
