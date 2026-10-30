package auth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-test-secret-test-secret-0123"

func TestHashAndVerifyPassword(t *testing.T) {
	h, err := HashPassword("Gizli123!")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("Gizli123!", h) {
		t.Error("doğru parola reddedildi")
	}
	if VerifyPassword("gizli123!", h) {
		t.Error("yanlış parola kabul edildi")
	}
	h2, _ := HashPassword("Gizli123!")
	if h == h2 {
		t.Error("aynı parola iki kez aynı özeti verdi: tuz rastgele olmalı")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, enc := range []string{
		"", "düz-metin", "$argon2id$", "$bcrypt$v=19$m=1,t=1,p=1$YQ$YQ",
		"$argon2id$v=19$m=999999999,t=1,p=1$YQ$YQ", // aşırı bellek talebi
		"$argon2id$v=19$m=65536,t=2,p=2$!!!$!!!",
	} {
		if VerifyPassword("x", enc) {
			t.Errorf("bozuk özet kabul edildi: %q", enc)
		}
	}
}

// Rastgele girdilerle VerifyPassword asla panik etmemeli.
func FuzzVerifyPassword(f *testing.F) {
	f.Add("pw", "$argon2id$v=19$m=8,t=1,p=1$YWJj$YWJj")
	f.Add("", "$$$$$")
	f.Fuzz(func(t *testing.T, pw, enc string) {
		_ = VerifyPassword(pw, enc)
	})
}

func TestAccessTokenRoundTrip(t *testing.T) {
	iss := NewIssuer(testSecret, time.Minute)
	tok, err := iss.Access(42, RoleAdvisor)
	if err != nil {
		t.Fatal(err)
	}
	p, err := iss.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != 42 || p.Role != RoleAdvisor {
		t.Errorf("beklenmeyen kimlik: %+v", p)
	}
}

func TestParseRejects(t *testing.T) {
	iss := NewIssuer(testSecret, time.Minute)

	t.Run("süresi dolmuş", func(t *testing.T) {
		old := NewIssuer(testSecret, time.Minute)
		old.now = func() time.Time { return time.Now().Add(-time.Hour) }
		tok, _ := old.Access(1, RoleStudent)
		if _, err := iss.Parse(tok); err == nil {
			t.Error("süresi dolmuş token kabul edildi")
		}
	})
	t.Run("yanlış anahtar", func(t *testing.T) {
		other := NewIssuer("baska-anahtar-baska-anahtar-baska-anahtar", time.Minute)
		tok, _ := other.Access(1, RoleStudent)
		if _, err := iss.Parse(tok); err == nil {
			t.Error("başka anahtarla imzalı token kabul edildi")
		}
	})
	t.Run("alg=none", func(t *testing.T) {
		c := Claims{Role: RoleRegistrar, RegisteredClaims: jwt.RegisteredClaims{
			Subject: "1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
		tok, _ := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
		if _, err := iss.Parse(tok); err == nil {
			t.Error("imzasız (alg=none) token kabul edildi")
		}
	})
	t.Run("HS512", func(t *testing.T) {
		c := Claims{Role: RoleRegistrar, RegisteredClaims: jwt.RegisteredClaims{
			Subject: "1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
		tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS512, c).SignedString([]byte(testSecret))
		if _, err := iss.Parse(tok); err == nil {
			t.Error("beklenmeyen algoritmayla imzalı token kabul edildi")
		}
	})
	t.Run("süresiz", func(t *testing.T) {
		c := Claims{Role: RoleStudent, RegisteredClaims: jwt.RegisteredClaims{Subject: "1"}}
		tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(testSecret))
		if _, err := iss.Parse(tok); err == nil {
			t.Error("exp alanı olmayan token kabul edildi")
		}
	})
}

func TestRefreshTokenHash(t *testing.T) {
	tok, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if hash != HashRefreshToken(tok) || hash == tok {
		t.Error("özet tutarsız")
	}
	tok2, _, _ := NewRefreshToken()
	if tok == tok2 {
		t.Error("token'lar benzersiz olmalı")
	}
}

func TestAuthenticateAndRequire(t *testing.T) {
	iss := NewIssuer(testSecret, time.Minute)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		_, _ = w.Write([]byte(strconv.FormatInt(p.UserID, 10)))
	})
	h := iss.Authenticate(Require(RoleRegistrar, RoleReporting)(ok))

	call := func(authz string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if got := call("").Code; got != http.StatusUnauthorized {
		t.Errorf("token yok: %d, beklenen 401", got)
	}
	if got := call("Bearer çöp").Code; got != http.StatusUnauthorized {
		t.Errorf("çöp token: %d, beklenen 401", got)
	}
	if got := call("Basic abc").Code; got != http.StatusUnauthorized {
		t.Errorf("yanlış şema: %d, beklenen 401", got)
	}

	student, _ := iss.Access(1, RoleStudent)
	if got := call("Bearer " + student).Code; got != http.StatusForbidden {
		t.Errorf("öğrenci: %d, beklenen 403", got)
	}

	registrar, _ := iss.Access(7, RoleRegistrar)
	rec := call("Bearer " + registrar)
	if rec.Code != http.StatusOK || rec.Body.String() != "7" {
		t.Errorf("öğrenci işleri: %d %q, beklenen 200 \"7\"", rec.Code, rec.Body.String())
	}
}
