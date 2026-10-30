// Package config tüm yapılandırmayı ortam değişkenlerinden okur.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	JWTSecret       string
	DocSigningKey   string
	PublicBaseURL   string
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	SeedDemo        bool
	DBMaxConns      int32
	HashConcurrency int
	ShutdownTimeout time.Duration
}

// Load ortam değişkenlerini okur; zorunlu olanlar eksikse hata döner.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:        getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		DocSigningKey:   os.Getenv("DOC_SIGNING_KEY"),
		PublicBaseURL:   getenv("PUBLIC_BASE_URL", "http://localhost:8080"),
		AccessTTL:       15 * time.Minute,
		RefreshTTL:      7 * 24 * time.Hour,
		SeedDemo:        getenv("SEED_DEMO", "true") == "true",
		DBMaxConns:      int32(atoi(getenv("DB_MAX_CONNS", "40"), 40)),
		HashConcurrency: atoi(getenv("HASH_CONCURRENCY", "4"), 4),
		ShutdownTimeout: 15 * time.Second,
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL zorunlu")
	}
	// HS256 için kısa anahtar kaba kuvvete açıktır; en az 32 bayt şart koşuyoruz.
	if len(c.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET en az 32 karakter olmalı")
	}
	// Belge imzası JWT anahtarından bağımsızdır: biri sızarsa diğeri etkilenmez.
	if len(c.DocSigningKey) < 32 {
		return Config{}, fmt.Errorf("DOC_SIGNING_KEY en az 32 karakter olmalı")
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// atoi sayıya çevirir; geçersiz ya da pozitif olmayan değerde varsayılanı döner.
func atoi(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}
