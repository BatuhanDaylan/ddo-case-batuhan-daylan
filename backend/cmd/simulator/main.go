// simulator: büyük ölçekli tohumlu test verisi üretir ve kayıt anı yükünü taklit eder (şartname Bölüm 8).
//
//	simulator seed                      12.000 öğrenci, 600 ders, 1.500 şube, 4 yıllık not geçmişi yükler
//	simulator tokens -n 5000 -out f     k6 için öğrenci token'larını ve şube kimliklerini JSON'a yazar
//	simulator storm -students 5000      kayıt anı: eşzamanlı kayıt istekleri (Go istemcisiyle)
//	simulator check                     kayıt anından sonra "kontenjan aşımı yok" doğrulamasını yapar
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/db"
	"github.com/batuhan-daylan/uninet/internal/simulator"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "kullanım: simulator <seed|tokens|storm|check> [bayraklar]")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		slog.Error("simulator", "err", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run(cmd string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	seed := fs.Uint64("seed", 42, "tohum: aynı tohum aynı veriyi üretir")
	n := fs.Int("n", 5000, "tokens: token üretilecek öğrenci sayısı")
	out := fs.String("out", "tokens.json", "tokens: çıktı dosyası")
	students := fs.Int("students", 5000, "storm: istek atan öğrenci sayısı")
	per := fs.Int("per-student", 3, "storm: öğrenci başına farklı şube sayısı")
	conc := fs.Int("concurrency", 200, "storm: eşzamanlı istek sayısı")
	api := fs.String("api", env("API_URL", "http://backend:8080"), "storm: API adresi")
	_ = fs.Parse(args)

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL zorunlu")
	}
	pool, err := db.Connect(ctx, dsn, 10)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch cmd {
	case "seed":
		hash, err := auth.HashPassword(auth.DemoPassword)
		if err != nil {
			return err
		}
		start := time.Now()
		d := simulator.Generate(*seed, simulator.Full, hash)
		for _, t := range d.Tables {
			slog.Info("üretildi", "tablo", t.Name, "satir", len(t.Rows))
		}
		if err := simulator.Load(ctx, pool, d); err != nil {
			if errors.Is(err, simulator.ErrAlreadySeeded) {
				slog.Warn(err.Error())
				return nil
			}
			return err
		}
		slog.Info("simülasyon verisi yüklendi", "tohum", *seed, "sure", time.Since(start).Round(time.Millisecond))
		return nil

	case "tokens":
		t, err := simulator.LoadTarget(ctx, pool, os.Getenv("JWT_SECRET"), *n)
		if err != nil {
			return err
		}
		if err := simulator.WriteTokens(*out, t); err != nil {
			return err
		}
		slog.Info("token dosyası yazıldı", "dosya", *out, "ogrenci", len(t.Students), "sube", len(t.Sections))
		return nil

	case "storm":
		t, err := simulator.LoadTarget(ctx, pool, os.Getenv("JWT_SECRET"), *students)
		if err != nil {
			return err
		}
		fmt.Printf("kayıt anı: %d öğrenci × %d şube isteği, %d eşzamanlı, %d hedef şube\n", len(t.Students), *per, *conc, len(t.Sections))
		res := simulator.Storm(ctx, *api, t, *per, *conc, *seed)
		fmt.Print(res)
		return nil

	case "check":
		v, err := simulator.CheckQuota(ctx, pool)
		if err != nil {
			return err
		}
		if len(v) == 0 {
			fmt.Println("PASS: kontenjan aşımı yok; sayaçlar, sıra ve bekleme listesi tutarlı")
			return nil
		}
		for _, x := range v {
			fmt.Printf("FAIL: %s (%d)\n", x.Check, x.Count)
		}
		return errors.New("kota doğrulaması başarısız")
	}
	return fmt.Errorf("bilinmeyen komut %q", cmd)
}
