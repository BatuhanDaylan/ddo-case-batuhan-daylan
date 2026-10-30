// Package audit değiştirilemez denetim kaydına yazar.
package audit

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgconn"
)

// Execer hem *pgxpool.Pool hem pgx.Tx tarafından sağlanır; böylece denetim kaydı
// iş mantığıyla aynı transaction içinde yazılabilir (ya ikisi de olur ya hiçbiri).
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Entry struct {
	ActorID  *int64
	Action   string
	Entity   string
	EntityID string
	Old      any
	New      any
	IP       string
}

func Write(ctx context.Context, ex Execer, e Entry) error {
	oldJSON, err := marshal(e.Old)
	if err != nil {
		return err
	}
	newJSON, err := marshal(e.New)
	if err != nil {
		return err
	}
	_, err = ex.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, entity, entity_id, old_value, new_value, ip)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''))`,
		e.ActorID, e.Action, e.Entity, e.EntityID, oldJSON, newJSON, e.IP)
	return err
}

// marshal nil değeri SQL NULL olarak bırakır.
func marshal(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
