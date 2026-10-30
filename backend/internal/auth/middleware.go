package auth

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/batuhan-daylan/uninet/internal/httpx"
)

// Principal isteği yapan kimliği temsil eder; yalnızca doğrulanmış token'dan üretilir.
type Principal struct {
	UserID int64
	Role   string
}

type ctxKey struct{}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Authenticate Bearer token'ı doğrular; yoksa/geçersizse 401 döner.
func (i *Issuer) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		raw, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || raw == "" {
			httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "kimlik doğrulama gerekli")
			return
		}
		p, err := i.Parse(raw)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "invalid_token", "token geçersiz veya süresi dolmuş")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

// Require yalnızca verilen rollere izin verir; diğerleri 403 alır.
// Authenticate'ten sonra kullanılmalıdır.
func Require(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "kimlik doğrulama gerekli")
				return
			}
			if !slices.Contains(roles, p.Role) {
				httpx.Error(w, http.StatusForbidden, "forbidden", "bu işlem için yetkiniz yok")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
