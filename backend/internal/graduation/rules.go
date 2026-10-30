// Package graduation mezuniyet denetimini yapar. Kurallar kodda sabit değildir: bir müfredat sürümüne
// bağlı JSON belgesi olarak saklanır ve burada doğrulanıp değerlendirilir.
package graduation

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Rules bir müfredat sürümünün mezuniyet kurallarıdır. Örnek:
//
//	{
//	  "min_gano": 2.0,
//	  "min_total_ects": 240,
//	  "required": ["BM101", "BM102"],            // verilmezse müfredattaki zorunlu dersler kullanılır
//	  "pools": [
//	    {"name": "Seçmeli A", "courses": ["BM301","BM302"], "min_courses": 3, "min_ects": 9}
//	  ],
//	  "internship": {"course": "BM399"}
//	}
type Rules struct {
	MinGANO      float64     `json:"min_gano"`
	MinTotalECTS int         `json:"min_total_ects"`
	Required     []string    `json:"required,omitempty"`
	Pools        []Pool      `json:"pools"`
	Internship   *Internship `json:"internship,omitempty"`
}

type Pool struct {
	Name       string   `json:"name"`
	Courses    []string `json:"courses"`
	MinCourses int      `json:"min_courses"`
	MinECTS    int      `json:"min_ects"`
}

type Internship struct {
	Course string `json:"course"`
}

// Sınırlar: çözücünün durum uzayını ve kötü niyetli girdiyi sınırlar.
const (
	maxPools      = 8
	maxMinCourses = 50
	maxMinECTS    = 500
	maxPerPool    = 200
)

// Validate kuralların yapısal geçerliliğini denetler (derslerin veritabanında varlığı ayrıca denetlenir).
func (r Rules) Validate() error {
	if r.MinGANO < 0 || r.MinGANO > 4 {
		return errors.New("min_gano 0-4 arasında olmalı")
	}
	if r.MinTotalECTS < 0 || r.MinTotalECTS > 1000 {
		return errors.New("min_total_ects 0-1000 arasında olmalı")
	}
	if len(r.Pools) > maxPools {
		return fmt.Errorf("en fazla %d havuz tanımlanabilir", maxPools)
	}
	if dup := firstDuplicate(r.Required); dup != "" {
		return fmt.Errorf("required içinde %q tekrar ediyor", dup)
	}
	for _, c := range r.Required {
		if strings.TrimSpace(c) == "" {
			return errors.New("required içinde boş ders kodu var")
		}
	}
	names := map[string]bool{}
	for i, p := range r.Pools {
		if strings.TrimSpace(p.Name) == "" || names[p.Name] {
			return fmt.Errorf("pools[%d]: ad boş veya tekrar ediyor", i)
		}
		names[p.Name] = true
		if len(p.Courses) == 0 || len(p.Courses) > maxPerPool {
			return fmt.Errorf("pools[%d] (%s): 1-%d ders içermeli", i, p.Name, maxPerPool)
		}
		if dup := firstDuplicate(p.Courses); dup != "" {
			return fmt.Errorf("pools[%d] (%s): %q tekrar ediyor", i, p.Name, dup)
		}
		if p.MinCourses < 0 || p.MinCourses > maxMinCourses || p.MinECTS < 0 || p.MinECTS > maxMinECTS {
			return fmt.Errorf("pools[%d] (%s): min_courses 0-%d, min_ects 0-%d olmalı", i, p.Name, maxMinCourses, maxMinECTS)
		}
		if p.MinCourses == 0 && p.MinECTS == 0 {
			return fmt.Errorf("pools[%d] (%s): min_courses veya min_ects pozitif olmalı", i, p.Name)
		}
	}
	if r.Internship != nil && strings.TrimSpace(r.Internship.Course) == "" {
		return errors.New("internship.course boş olamaz")
	}
	return nil
}

// AllCodes kurallarda geçen tüm ders kodlarını (tekrarsız, sıralı) verir.
func (r Rules) AllCodes() []string {
	set := map[string]bool{}
	for _, c := range r.Required {
		set[c] = true
	}
	for _, p := range r.Pools {
		for _, c := range p.Courses {
			set[c] = true
		}
	}
	if r.Internship != nil {
		set[r.Internship.Course] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

func firstDuplicate(xs []string) string {
	seen := map[string]bool{}
	for _, x := range xs {
		if seen[x] {
			return x
		}
		seen[x] = true
	}
	return ""
}
