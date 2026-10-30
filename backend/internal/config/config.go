// Package config tüm yapılandırmayı ortam değişkenlerinden okur.
package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	ShutdownTimeout time.Duration
}

// Load ortam değişkenlerini okur; zorunlu olanlar eksikse hata döner.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:        getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		ShutdownTimeout: 15 * time.Second,
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL zorunlu")
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
