// Package idempotency durum değiştiren POST isteklerini Idempotency-Key ile tekrar-güvenli yapar.
package idempotency

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrKeyReuse aynı anahtarın farklı bir istek için kullanıldığını belirtir.
var ErrKeyReuse = errors.New("idempotency key farklı bir istekle kullanıldı")

type Result struct {
	Status   int
	Body     []byte
	Replayed bool
}

// Do, run'ı en fazla bir kez çalıştırır. Anahtar satırı iş mantığıyla AYNI transaction'da
// yazıldığı için:
//   - run başarısız olursa anahtar da geri alınır, istemci güvenle yeniden deneyebilir;
//   - aynı anahtarla eşzamanlı gelen ikinci istek, ilki commit olana kadar INSERT'te bekler,
//     sonra çakışmayı görüp ilkinin kaydettiği yanıtı aynen döner.
//
// run, beklenen iş sonuçlarını (4xx dahil) hata değil (status, body) olarak döndürmelidir;
// yalnızca beklenmeyen hatalar error olmalıdır.
func Do(ctx context.Context, pool *pgxpool.Pool, userID int64, key, fingerprint string,
	run func(tx pgx.Tx) (status int, body []byte, err error)) (Result, error) {

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx,
		`INSERT INTO idempotency_keys (user_id, key, fingerprint) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, key) DO NOTHING`, userID, key, fingerprint)
	if err != nil {
		return Result{}, err
	}

	if ct.RowsAffected() == 0 {
		var fp string
		var status *int
		var body []byte
		if err := tx.QueryRow(ctx,
			`SELECT fingerprint, status_code, response_body FROM idempotency_keys WHERE user_id = $1 AND key = $2`,
			userID, key).Scan(&fp, &status, &body); err != nil {
			return Result{}, err
		}
		if fp != fingerprint {
			return Result{}, ErrKeyReuse
		}
		if status == nil {
			return Result{}, errors.New("idempotency kaydı tamamlanmamış")
		}
		return Result{Status: *status, Body: body, Replayed: true}, nil
	}

	status, body, err := run(tx)
	if err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE idempotency_keys SET status_code = $3, response_body = $4 WHERE user_id = $1 AND key = $2`,
		userID, key, status, body); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{Status: status, Body: body}, nil
}
