-- +goose Up
-- Refresh token'lar düz metin değil, SHA-256 özeti olarak saklanır:
-- veritabanı sızsa bile token'lar kullanılamaz.
CREATE TABLE refresh_tokens (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT        NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);

-- Denetim kaydı: kim, ne zaman, neyi, eski/yeni değer.
CREATE TABLE audit_log (
    id        BIGSERIAL PRIMARY KEY,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_id  BIGINT,
    action    TEXT        NOT NULL,
    entity    TEXT        NOT NULL,
    entity_id TEXT,
    old_value JSONB,
    new_value JSONB,
    ip        TEXT
);
CREATE INDEX audit_log_entity_idx ON audit_log (entity, entity_id);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, at);

-- Değiştirilemezlik uygulama koduna değil veritabanına emanet edilir:
-- UPDATE/DELETE/TRUNCATE denemesi, çağıran kim olursa olsun hata verir.
-- +goose StatementBegin
CREATE FUNCTION audit_log_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log değiştirilemez (append-only)';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable();

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION audit_log_immutable();
DROP TABLE refresh_tokens;
