package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Querier hem *pgxpool.Pool hem pgx.Tx tarafından sağlanır.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Each sorguyu çalıştırır, her satır için scan'i çağırır; satır kümesini her durumda kapatır ve
// yineleme hatasını döner. "Sorgu aç, tara, kapat, hata bak" kalıbını tek yerde tutar.
func Each(ctx context.Context, q Querier, scan func(pgx.Rows) error, sql string, args ...any) error {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
