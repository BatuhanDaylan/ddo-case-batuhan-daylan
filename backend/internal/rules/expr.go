// Package rules kayıt kurallarının saf (veritabanından bağımsız) mantığını içerir:
// ön koşul ifadeleri, ön koşul grafiğinde döngü tespiti ve kayıt doğrulayıcısı.
package rules

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const (
	OpAnd = "and"
	OpOr  = "or"

	maxDepth = 8
	maxNodes = 64
)

// Node bir ön koşul ifade ağacının düğümüdür. Ya yapraktır (CourseID > 0) ya da
// "and"/"or" işlemidir (Args dolu).
type Node struct {
	Op       string `json:"op,omitempty"`
	CourseID int64  `json:"course_id,omitempty"`
	Args     []Node `json:"args,omitempty"`
}

// Validate ifadenin yapısal olarak geçerli olduğunu denetler. Girdi dışarıdan (JSON) geldiği için
// derinlik ve düğüm sayısı sınırlıdır; böylece kötü niyetli iç içe ifade özyinelemeyi şişiremez.
func (n Node) Validate() error {
	count := 0
	return n.validate(1, &count)
}

func (n Node) validate(depth int, count *int) error {
	if depth > maxDepth {
		return fmt.Errorf("ifade en fazla %d seviye iç içe olabilir", maxDepth)
	}
	if *count++; *count > maxNodes {
		return fmt.Errorf("ifade en fazla %d düğüm içerebilir", maxNodes)
	}
	switch {
	case n.Op == "" && n.CourseID > 0:
		if len(n.Args) != 0 {
			return errors.New("yaprak düğümün alt ifadesi olamaz")
		}
		return nil
	case n.Op == OpAnd || n.Op == OpOr:
		if n.CourseID != 0 {
			return errors.New("işlem düğümü course_id taşıyamaz")
		}
		if len(n.Args) == 0 {
			return fmt.Errorf("%q işleminin en az bir alt ifadesi olmalı", n.Op)
		}
		for _, a := range n.Args {
			if err := a.validate(depth+1, count); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("düğüm ya course_id (>0) ya da op (and|or) içermeli")
	}
}

// Eval ifadenin, geçilmiş dersler kümesiyle sağlanıp sağlanmadığını döner.
func (n Node) Eval(passed map[int64]bool) bool {
	switch n.Op {
	case OpAnd:
		for _, a := range n.Args {
			if !a.Eval(passed) {
				return false
			}
		}
		return true
	case OpOr:
		for _, a := range n.Args {
			if a.Eval(passed) {
				return true
			}
		}
		return false
	default:
		return passed[n.CourseID]
	}
}

// Courses ifadede geçen ders kimliklerini (tekrarsız, sıralı) döner.
func (n Node) Courses() []int64 {
	set := map[int64]bool{}
	n.collect(set)
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func (n Node) collect(set map[int64]bool) {
	if n.Op == "" {
		set[n.CourseID] = true
		return
	}
	for _, a := range n.Args {
		a.collect(set)
	}
}

// Describe ifadeyi insan okunur yazar: "BM101 ve (MAT101 veya FIZ101)".
func (n Node) Describe(codes map[int64]string) string {
	if n.Op == "" {
		if c, ok := codes[n.CourseID]; ok {
			return c
		}
		return fmt.Sprintf("#%d", n.CourseID)
	}
	word := " ve "
	if n.Op == OpOr {
		word = " veya "
	}
	parts := make([]string, len(n.Args))
	for i, a := range n.Args {
		s := a.Describe(codes)
		if a.Op != "" && a.Op != n.Op && len(a.Args) > 1 {
			s = "(" + s + ")"
		}
		parts[i] = s
	}
	return strings.Join(parts, word)
}

// DetectCycle ön koşul grafiğinde (ders → ön koşul dersleri) döngü arar.
// Döngü varsa döngüdeki ders kimliklerini sırayla (ilk eleman sonda tekrarlanır) döner, yoksa nil.
// Sonuç deterministiktir (anahtarlar sıralı gezilir).
func DetectCycle(prereqs map[int64]Node) []int64 {
	const (
		white = iota
		gray
		black
	)
	color := map[int64]int{}
	var stack []int64
	var cycle []int64

	var visit func(id int64) bool
	visit = func(id int64) bool {
		color[id] = gray
		stack = append(stack, id)
		if expr, ok := prereqs[id]; ok {
			for _, dep := range expr.Courses() {
				switch color[dep] {
				case gray:
					i := slices.Index(stack, dep)
					cycle = append(slices.Clone(stack[i:]), dep)
					return true
				case white:
					if visit(dep) {
						return true
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}

	keys := make([]int64, 0, len(prereqs))
	for id := range prereqs {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, id := range keys {
		if color[id] == white && visit(id) {
			return cycle
		}
	}
	return nil
}
