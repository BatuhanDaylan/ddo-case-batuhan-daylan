package registration

import (
	"slices"
	"testing"
)

func TestRiskReasons(t *testing.T) {
	cases := []struct {
		name string
		in   riskInput
		want []string
	}{
		{"sağlıklı", riskInput{GANO: 3.0, LastTermGPA: 3.1, PrevTermGPA: 3.0, HasPrevTerm: true}, nil},
		{"düşük GANO", riskInput{GANO: 1.8, LastTermGPA: 1.8}, []string{"low_gano"}},
		{"GANO tam eşikte risk değil", riskInput{GANO: 2.0, LastTermGPA: 2.0}, nil},
		{"dönem düşüşü", riskInput{GANO: 2.8, LastTermGPA: 2.0, PrevTermGPA: 2.6, HasPrevTerm: true}, []string{"gpa_drop"}},
		{"düşüş eşiğin altında", riskInput{GANO: 2.8, LastTermGPA: 2.2, PrevTermGPA: 2.6, HasPrevTerm: true}, nil},
		{"önceki dönem yoksa düşüş sayılmaz", riskInput{GANO: 2.5, LastTermGPA: 0.5}, nil},
		{"iki dersten kalma", riskInput{GANO: 2.5, LastTermGPA: 2.5, FailedInLast: 2}, []string{"failed_courses"}},
		{"hepsi", riskInput{GANO: 1.5, LastTermGPA: 0.8, PrevTermGPA: 2.0, HasPrevTerm: true, FailedInLast: 3},
			[]string{"low_gano", "gpa_drop", "failed_courses"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := riskReasons(c.in); !slices.Equal(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
