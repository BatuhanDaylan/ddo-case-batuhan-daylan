package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/batuhan-daylan/uninet/internal/testdb"
)

// /me: kullanıcı gerçekten yoksa 404; veritabanı ulaşılamıyorsa "kullanıcı yok" DEĞİL, yeniden denenebilir 503 olmalı.
func TestMeDistinguishesMissingUserFromOutage(t *testing.T) {
	pool := testdb.New(t)
	iss := NewIssuer("integration-test-secret-integration-test", time.Hour)
	h, err := NewHandler(pool, iss, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, full_name, role) VALUES ('a@t', 'x', 'A', 'instructor') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	call := func(userID int64) *httptest.ResponseRecorder {
		tok, _ := iss.Access(userID, RoleInstructor)
		req := httptest.NewRequest("GET", "/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		iss.Authenticate(http.HandlerFunc(h.Me)).ServeHTTP(rec, req)
		return rec
	}

	if rec := call(uid); rec.Code != http.StatusOK {
		t.Fatalf("var olan kullanıcı: %d %s", rec.Code, rec.Body)
	}
	if rec := call(999999); rec.Code != http.StatusNotFound {
		t.Errorf("olmayan kullanıcı: %d, beklenen 404", rec.Code)
	}

	pool.Close() // veritabanı kullanılamaz hale geldi
	rec := call(uid)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("veritabanı kesintisi: %d Retry-After=%q, beklenen 503 + Retry-After (404 olmamalı)", rec.Code, rec.Header().Get("Retry-After"))
	}
}
