package documents

import (
	"bytes"
	"strings"
	"testing"
)

const testKey = "belge-imza-anahtari-belge-imza-anahtari"

func TestSignVerifyRoundTrip(t *testing.T) {
	s, err := NewSigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	sha := SHA256Hex([]byte(`{"gano":3.5}`))
	code := s.Code(42, sha)
	id, sig, err := ParseCode(code)
	if err != nil || id != 42 {
		t.Fatalf("ParseCode: id=%d err=%v", id, err)
	}
	if !s.Verify(id, sig, sha) {
		t.Error("geçerli imza reddedildi")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	s, _ := NewSigner(testKey)
	sha := SHA256Hex([]byte("özgün içerik"))
	code := s.Code(7, sha)
	id, sig, _ := ParseCode(code)

	if s.Verify(id, sig, SHA256Hex([]byte("değiştirilmiş içerik"))) {
		t.Error("içerik değişti ama imza geçerli sayıldı")
	}
	if s.Verify(id+1, sig, sha) {
		t.Error("imza başka bir belge kimliğiyle geçerli sayıldı")
	}
	other, _ := NewSigner("baska-anahtar-baska-anahtar-baska-anahtar")
	if other.Verify(id, sig, sha) {
		t.Error("başka anahtarla doğrulama geçti")
	}
	// İmzanın tek bir biti bozulursa geçmemeli.
	bad := append([]byte(nil), sig...)
	bad[0] ^= 1
	if s.Verify(id, bad, sha) {
		t.Error("bozulmuş imza geçerli sayıldı")
	}
}

func TestParseCodeRejectsGarbage(t *testing.T) {
	for _, c := range []string{
		"", "abc", "1", "1.", ".abc", "x.abc", "0.AAAA", "-5.AAAA", "1.!!!", "1.AAAA",
		strings.Repeat("9", 200) + ".AAAA", "1.2.3",
	} {
		if _, _, err := ParseCode(c); err == nil {
			t.Errorf("geçersiz kod kabul edildi: %q", c)
		}
	}
}

func TestNewSignerRejectsShortKey(t *testing.T) {
	if _, err := NewSigner("kisa"); err == nil {
		t.Error("kısa anahtar kabul edildi")
	}
}

// Fuzz: rastgele kod dizeleri ayrıştırılırken asla panik olmamalı.
func FuzzParseCode(f *testing.F) {
	f.Add("42.AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AbCdEfG")
	f.Add("1.")
	f.Fuzz(func(t *testing.T, code string) {
		_, _, _ = ParseCode(code)
	})
}

// İmzanın son base64 karakteri 4 bit veri + 2 kullanılmayan bit taşır. Kullanılmayan bitler farklı olan kodlar
// aynı imzaya çözülebilirdi; ParseCode aynı imzayı temsil eden YALNIZCA tek yazımı kabul etmeli.
func TestParseCodeRejectsNonCanonicalSignature(t *testing.T) {
	s, _ := NewSigner(strings.Repeat("k", 32))
	code := s.Code(7, SHA256Hex([]byte("içerik")))
	_, want, err := ParseCode(code)
	if err != nil {
		t.Fatalf("kanonik kod reddedildi: %v", err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	same := 0
	for _, c := range alphabet {
		_, got, err := ParseCode(code[:len(code)-1] + string(c))
		if err == nil && bytes.Equal(got, want) {
			same++
		}
	}
	if same != 1 {
		t.Fatalf("aynı imzayı temsil eden %d farklı yazım kabul edildi, beklenen yalnızca 1", same)
	}
}
