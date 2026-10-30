// Package auth kimlik doğrulama, token üretimi ve rol tabanlı yetkilendirmeyi içerir.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parametreleri (OWASP'ın önerdiği aralıkta): 64 MiB bellek, 2 iterasyon.
const (
	argonMemory  = 64 * 1024
	argonTime    = 2
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword parolayı rastgele tuzla argon2id ile özetler.
// Çıktı biçimi: $argon2id$v=19$m=65536,t=2,p=2$<salt>$<hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword parolayı kayıtlı özetle sabit zamanlı karşılaştırır.
// Bozuk/beklenmedik biçimdeki özetler için panik etmez, false döner.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	// Saldırgan kontrollü bir özet devasa bellek istemesin.
	if memory == 0 || memory > 256*1024 || iterations == 0 || iterations > 10 || threads == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
