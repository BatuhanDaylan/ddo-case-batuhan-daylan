-- +goose Up
-- Mezuniyet kuralları veridir: müfredat sürümüne bağlı JSON belgesi (kodda sabit değil).
-- Müfredat sürümü gibi, kurallar da öğrencinin GİRİŞ yılına göre çözülür.
CREATE TABLE graduation_rules (
    curriculum_version_id BIGINT PRIMARY KEY REFERENCES curriculum_versions(id),
    rules      JSONB  NOT NULL,
    updated_by BIGINT REFERENCES users(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE graduation_rules;
