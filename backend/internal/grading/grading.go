// Package grading not hesaplamanın saf (veritabanından bağımsız) mantığını içerir:
// ağırlıklı toplam, mutlak ve bağıl (T-skoru) harf notu, geçerli not seçimi ve GANO.
package grading

import (
	"math"
	"sort"
)

// Harf notu ölçeği: ad, katsayı. FF dışındakiler geçer (>= DD).
type Letter struct {
	Name  string
	Point float64
}

var Scale = []Letter{
	{"AA", 4.0}, {"BA", 3.5}, {"BB", 3.0}, {"CB", 2.5}, {"CC", 2.0}, {"DC", 1.5}, {"DD", 1.0}, {"FF", 0.0},
}

const passPoint = 1.0

func PointOf(letter string) (float64, bool) {
	for _, l := range Scale {
		if l.Name == letter {
			return l.Point, true
		}
	}
	return 0, false
}

func Passed(letter string) bool {
	p, ok := PointOf(letter)
	return ok && p >= passPoint
}

// Bileşenler ve sınav türleri.
const (
	Midterm  = "midterm"
	Final    = "final"
	Homework = "homework"

	KindRegular = "regular"
	KindMakeup  = "makeup" // bütünleme
	KindExcuse  = "excuse" // mazeret sınavı
)

var Components = []string{Midterm, Final, Homework}

// Yöntemler.
const (
	MethodAbsolute = "absolute"
	MethodRelative = "relative"
)

// Mutlak not eşikleri (toplam >= eşik).
var absoluteCuts = []struct {
	min    float64
	letter string
}{
	{90, "AA"}, {85, "BA"}, {80, "BB"}, {75, "CB"}, {65, "CC"}, {58, "DC"}, {50, "DD"},
}

// Bağıl değerlendirmede T-skoru eşikleri (T >= eşik).
var relativeCuts = []struct {
	min    float64
	letter string
}{
	{65, "AA"}, {60, "BA"}, {55, "BB"}, {50, "CB"}, {45, "CC"}, {40, "DC"}, {35, "DD"},
}

// Bağıl yöntemde bile bu toplamın altı doğrudan FF'dir: sınıf ortalaması çok düşükse
// T-skoru kimseyi hak etmediği bir notla geçirmesin.
const relativeFloor = 40.0

// AbsoluteLetter toplam puandan (0-100) mutlak harf notunu verir.
func AbsoluteLetter(total float64) string {
	for _, c := range absoluteCuts {
		if total >= c.min {
			return c.letter
		}
	}
	return "FF"
}

// Effective, bileşen başına birden çok sınav türünden geçerli olanı seçer.
// Öncelik: bütünleme > mazeret > normal. Girdi: component → kind → puan.
func Effective(scores map[string]map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for comp, byKind := range scores {
		for _, k := range []string{KindMakeup, KindExcuse, KindRegular} {
			if v, ok := byKind[k]; ok {
				out[comp] = v
				break
			}
		}
	}
	return out
}

// Total ağırlıklı toplamı verir (ağırlıklar yüzde; toplamları 100 olmalı).
// Puanı girilmemiş bileşen 0 sayılır. Sonuç 2 ondalığa yuvarlanır.
func Total(weights map[string]int, effective map[string]float64) float64 {
	sum := 0.0
	for comp, w := range weights {
		sum += float64(w) * effective[comp] / 100
	}
	return round(sum, 2)
}

// MeanStd popülasyon ortalaması ve standart sapmasını verir.
func MeanStd(xs []float64) (mean, sd float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		sd += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(sd / float64(len(xs)))
}

// TScore T = 50 + 10·(x − ortalama)/sapma. Sapma 0 ise herkes 50 alır.
func TScore(x, mean, sd float64) float64 {
	if sd == 0 {
		return 50
	}
	return 50 + 10*(x-mean)/sd
}

// RelativeLetters sınıfın toplam puanlarından bağıl harf notlarını verir (girdiyle aynı sırada).
func RelativeLetters(totals []float64) []string {
	mean, sd := MeanStd(totals)
	out := make([]string, len(totals))
	for i, x := range totals {
		out[i] = relativeLetter(x, TScore(x, mean, sd))
	}
	return out
}

func relativeLetter(total, t float64) string {
	if total < relativeFloor {
		return "FF"
	}
	for _, c := range relativeCuts {
		if t >= c.min {
			return c.letter
		}
	}
	return "FF"
}

// Letters, yönteme göre bir şubenin tüm toplam puanlarını harf notuna çevirir.
func Letters(method string, totals []float64) []string {
	if method == MethodRelative {
		return RelativeLetters(totals)
	}
	out := make([]string, len(totals))
	for i, t := range totals {
		out[i] = AbsoluteLetter(t)
	}
	return out
}

// Tekrar alınan derslerde hangi denemenin GANO'ya sayılacağı.
const (
	PolicyLast = "last" // en son alınan not
	PolicyBest = "best" // en yüksek not
)

// Attempt bir dersin bir denemesidir. TermOrder dönemin kronolojik sırasıdır.
type Attempt struct {
	CourseID  int64
	TermOrder int64
	ECTS      int
	Point     float64
	Exempt    bool
}

// Gano denemelerden, politikaya göre ders başına tek deneme seçip AKTS ağırlıklı ortalamayı verir.
// Muaf dersler hesaba girmez. Sonuç 2 ondalığa yuvarlanır; hiç deneme yoksa 0.
func Gano(attempts []Attempt, policy string) float64 {
	chosen := map[int64]Attempt{}
	for _, a := range attempts {
		if a.Exempt {
			continue
		}
		cur, ok := chosen[a.CourseID]
		switch {
		case !ok:
			chosen[a.CourseID] = a
		case policy == PolicyBest && a.Point > cur.Point,
			policy != PolicyBest && a.TermOrder > cur.TermOrder:
			chosen[a.CourseID] = a
		}
	}
	ids := make([]int64, 0, len(chosen))
	for id := range chosen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var weighted float64
	var credits int
	for _, id := range ids {
		a := chosen[id]
		weighted += a.Point * float64(a.ECTS)
		credits += a.ECTS
	}
	if credits == 0 {
		return 0
	}
	return round(weighted/float64(credits), 2)
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}
