package httpx

import (
	"net/http"
	"strconv"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// PageParams keyset sayfalama parametrelerini okur: ?after=<son id>&limit=<n>.
// OFFSET yerine "id > after" kullanılır; büyük tablolarda da sabit maliyetlidir.
func PageParams(r *http.Request) (after int64, limit int, ok bool) {
	limit = defaultLimit
	q := r.URL.Query()
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		after = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			return 0, 0, false
		}
		limit = n
	}
	return after, limit, true
}

// Page liste yanıtının sabit biçimi.
type Page[T any] struct {
	Items     []T    `json:"items"`
	NextAfter *int64 `json:"next_after"`
}

// NewPage limit+1 satır çekilmişse fazlalığı atıp next_after'ı doldurur.
func NewPage[T any](items []T, limit int, id func(T) int64) Page[T] {
	p := Page[T]{Items: items}
	if len(items) > limit {
		p.Items = items[:limit]
		next := id(items[limit-1])
		p.NextAfter = &next
	}
	if p.Items == nil {
		p.Items = []T{}
	}
	return p
}
