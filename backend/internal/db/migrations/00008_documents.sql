-- +goose Up
-- Verilen belgeler. snapshot, belgenin düzenlendiği andaki içeriğin TAM baytlarıdır (TEXT):
-- JSONB anahtarları yeniden sıralayıp boşlukları değiştireceği için özet tutmazdı.
-- İmza (HMAC) veritabanında değil, belgenin kodundadır; veritabanı değişirse doğrulama başarısız olur.
CREATE TABLE documents (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT   NOT NULL CHECK (kind IN ('transcript')),
    student_id  BIGINT NOT NULL REFERENCES students(id),
    snapshot    TEXT   NOT NULL,
    snapshot_sha256 TEXT NOT NULL,
    issued_by   BIGINT NOT NULL REFERENCES users(id),
    issued_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX documents_student_idx ON documents (student_id, id);

-- Verilmiş belge değiştirilemez ve silinemez.
CREATE TRIGGER documents_no_update_delete
    BEFORE UPDATE OR DELETE ON documents
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
CREATE TRIGGER documents_no_truncate
    BEFORE TRUNCATE ON documents
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable();

-- +goose Down
DROP TABLE documents;
