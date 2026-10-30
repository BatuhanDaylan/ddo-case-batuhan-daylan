package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestPageParams(t *testing.T) {
	cases := []struct {
		url       string
		after     int64
		limit     int
		wantValid bool
	}{
		{"/x", 0, 50, true},
		{"/x?after=10&limit=5", 10, 5, true},
		{"/x?limit=200", 0, 200, true},
		{"/x?limit=201", 0, 0, false},
		{"/x?limit=0", 0, 0, false},
		{"/x?after=-1", 0, 0, false},
		{"/x?after=abc", 0, 0, false},
	}
	for _, c := range cases {
		after, limit, ok := PageParams(httptest.NewRequest("GET", c.url, nil))
		if ok != c.wantValid || (ok && (after != c.after || limit != c.limit)) {
			t.Errorf("%s: (%d,%d,%v), beklenen (%d,%d,%v)", c.url, after, limit, ok, c.after, c.limit, c.wantValid)
		}
	}
}

func TestNewPage(t *testing.T) {
	id := func(n int64) int64 { return n }

	// limit=2 için 3 satır çekilmiş: sayfa dolu ve devamı var.
	p := NewPage([]int64{1, 2, 3}, 2, id)
	if len(p.Items) != 2 || p.NextAfter == nil || *p.NextAfter != 2 {
		t.Errorf("devamı olan sayfa yanlış: %+v", p)
	}
	// Son sayfa: next_after null olmalı.
	p = NewPage([]int64{1, 2}, 2, id)
	if len(p.Items) != 2 || p.NextAfter != nil {
		t.Errorf("son sayfa yanlış: %+v", p)
	}
	// Boş sonuç JSON'da null değil [] olmalı.
	if p = NewPage[int64](nil, 2, id); p.Items == nil {
		t.Error("boş liste nil kaldı")
	}
}
