// Package documents imzalı, doğrulanabilir belgeleri (transkript PDF'i + QR) üretir ve doğrular.
package documents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidCode = errors.New("belge kodu geçersiz")

// SHA256Hex bir içeriğin SHA-256 özetini onaltılık olarak verir.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Signer belge kodlarını HMAC-SHA256 ile imzalar ve doğrular.
//
// Kod biçimi: "<belge id>.<imza>". İmza, "uninet-doc-v1\n<id>\n<içerik özeti>" üzerinden hesaplanır.
// İçerik özetini imzaya katmak, veritabanındaki içerik sonradan değişirse imzanın tutmamasını sağlar.
// Belge kimliğini katmak, bir belgenin imzasının başka bir belge için kullanılmasını engeller.
type Signer struct{ key []byte }

func NewSigner(key string) (*Signer, error) {
	if len(key) < 32 {
		return nil, errors.New("belge imzalama anahtarı en az 32 karakter olmalı")
	}
	return &Signer{key: []byte(key)}, nil
}

func (s *Signer) mac(id int64, contentSHA string) []byte {
	m := hmac.New(sha256.New, s.key)
	fmt.Fprintf(m, "uninet-doc-v1\n%d\n%s", id, contentSHA)
	return m.Sum(nil)
}

// Code belge için doğrulama kodunu üretir.
func (s *Signer) Code(id int64, contentSHA string) string {
	return strconv.FormatInt(id, 10) + "." + base64.RawURLEncoding.EncodeToString(s.mac(id, contentSHA))
}

// ParseCode kodu belge kimliği ve imzaya ayırır; biçim bozuksa ErrInvalidCode döner.
func ParseCode(code string) (id int64, sig []byte, err error) {
	left, right, ok := strings.Cut(code, ".")
	if !ok || left == "" || right == "" || len(code) > 128 {
		return 0, nil, ErrInvalidCode
	}
	id, err = strconv.ParseInt(left, 10, 64)
	if err != nil || id < 1 {
		return 0, nil, ErrInvalidCode
	}
	// Strict: 43 karakterin son karakterindeki kullanılmayan 2 bit sıfır olmak zorunda. Aksi halde aynı imzayı temsil eden
	// 4 farklı kod olurdu (imza biçimi esnekliği); tek geçerli yazım olsun.
	sig, err = base64.RawURLEncoding.Strict().DecodeString(right)
	if err != nil || len(sig) != sha256.Size {
		return 0, nil, ErrInvalidCode
	}
	return id, sig, nil
}

// Verify, verilen imzanın belgenin ŞU ANKİ içeriğiyle eşleştiğini sabit zamanlı karşılaştırmayla doğrular.
func (s *Signer) Verify(id int64, sig []byte, contentSHA string) bool {
	return hmac.Equal(sig, s.mac(id, contentSHA))
}
