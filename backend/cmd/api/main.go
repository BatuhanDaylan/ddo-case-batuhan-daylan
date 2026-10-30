package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/academic"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/config"
	"github.com/batuhan-daylan/uninet/internal/db"
	"github.com/batuhan-daylan/uninet/internal/documents"
	"github.com/batuhan-daylan/uninet/internal/gradebook"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/registration"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("uygulama durdu", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(pool); err != nil {
		return err
	}
	log.Info("migrasyonlar uygulandı")

	if cfg.SeedDemo {
		if err := auth.SeedDemoUsers(ctx, pool); err != nil {
			return err
		}
		if err := academic.SeedDemo(ctx, pool); err != nil {
			return err
		}
		if err := registration.SeedDemo(ctx, pool); err != nil {
			return err
		}
		log.Info("demo kullanıcılar, akademik yapı ve dönem/şubeler hazır")
	}

	iss := auth.NewIssuer(cfg.JWTSecret, cfg.AccessTTL)
	authH, err := auth.NewHandler(pool, iss, cfg.RefreshTTL)
	if err != nil {
		return err
	}

	signer, err := documents.NewSigner(cfg.DocSigningKey)
	if err != nil {
		return err
	}
	docH := documents.NewHandler(pool, documents.NewService(pool, signer, cfg.PublicBaseURL))

	// GANO yeniden hesaplama işçisi; ctx iptal edilince (kapanış sinyali) durur.
	go gradebook.RunWorker(ctx, pool, time.Second, log)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: newRouter(pool, iss, authH, academic.NewHandler(pool), registration.NewHandler(pool),
			gradebook.NewHandler(pool), docH),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http dinleniyor", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("kapanış sinyali alındı")
	}

	// Graceful shutdown: süren istekler bitene kadar (en fazla ShutdownTimeout) bekle.
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(sctx)
}

func newRouter(pool *pgxpool.Pool, iss *auth.Issuer, authH *auth.Handler,
	acad *academic.Handler, reg *registration.Handler, gb *gradebook.Handler,
	docs *documents.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(25 * time.Second))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.Error(w, http.StatusNotFound, "not_found", "kaynak bulunamadı")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.Error(w, http.StatusMethodNotAllowed, "method_not_allowed", "yöntem desteklenmiyor")
	})

	r.Get("/health", healthHandler(pool))
	docs.PublicRoutes(r) // GET /verify/{code}: kimlik gerektirmez, kodun HMAC imzası yetkidir

	r.Route("/auth", func(r chi.Router) {
		r.Post("/login", authH.Login)
		r.Post("/refresh", authH.Refresh)
		r.Post("/logout", authH.Logout)
	})

	// Bu noktadan sonraki her uç nokta kimlik doğrulaması ister; rol kısıtı Require ile eklenir.
	r.Group(func(r chi.Router) {
		r.Use(iss.Authenticate)
		r.Get("/me", authH.Me)
		acad.Routes(r)
		reg.Routes(r)
		gb.Routes(r)
		docs.Routes(r)
	})
	return r
}

// healthHandler veritabanı bağlantısını da kontrol eder.
func healthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "db_unavailable", "veritabanına ulaşılamıyor")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "ok"})
	}
}
