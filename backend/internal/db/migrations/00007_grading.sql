-- +goose Up
ALTER TABLE sections
    ADD COLUMN grading_method TEXT NOT NULL DEFAULT 'absolute' CHECK (grading_method IN ('absolute', 'relative')),
    ADD COLUMN published_at   TIMESTAMPTZ;

-- Tekrar alınan derslerde GANO politikası program başına seçilir.
ALTER TABLE programs
    ADD COLUMN retake_policy TEXT NOT NULL DEFAULT 'last' CHECK (retake_policy IN ('last', 'best'));

-- students.gano bir ÖNBELLEKTİR; asıl doğruluk course_results'tadır. Arka plan job'ı yeniden hesaplar.
ALTER TABLE students
    ADD COLUMN gano NUMERIC(4, 2) NOT NULL DEFAULT 0,
    ADD COLUMN gano_updated_at TIMESTAMPTZ;

CREATE TABLE grade_components (
    section_id BIGINT  NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
    name       TEXT    NOT NULL CHECK (name IN ('midterm', 'final', 'homework')),
    weight     INTEGER NOT NULL CHECK (weight BETWEEN 1 AND 100),
    PRIMARY KEY (section_id, name)
);

-- Bir bileşen için normal, bütünleme ve mazeret puanı ayrı ayrı tutulur; geçerli olan
-- (bütünleme > mazeret > normal) hesaplamada seçilir.
CREATE TABLE component_scores (
    enrollment_id BIGINT NOT NULL REFERENCES enrollments(id),
    component     TEXT   NOT NULL CHECK (component IN ('midterm', 'final', 'homework')),
    kind          TEXT   NOT NULL CHECK (kind IN ('regular', 'makeup', 'excuse')),
    score         NUMERIC(5, 2) NOT NULL CHECK (score BETWEEN 0 AND 100),
    entered_by    BIGINT NOT NULL REFERENCES users(id),
    entered_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (enrollment_id, component, kind)
);

-- İlanla oluşan sonuç satırı hangi kayıttan türediğini bilir.
ALTER TABLE course_results ADD COLUMN enrollment_id BIGINT UNIQUE REFERENCES enrollments(id);

-- Not düzeltmesi: talep eden (öğretim üyesi) ile onaylayan (bölüm sekreteri/öğrenci işleri) AYRI kişilerdir.
CREATE TABLE grade_corrections (
    id           BIGSERIAL PRIMARY KEY,
    result_id    BIGINT NOT NULL REFERENCES course_results(id),
    requested_by BIGINT NOT NULL REFERENCES users(id),
    old_letter   TEXT   NOT NULL,
    new_letter   TEXT   NOT NULL,
    reason       TEXT   NOT NULL,
    status       TEXT   NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    decided_by   BIGINT REFERENCES users(id),
    decided_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (old_letter <> new_letter),
    CHECK (decided_by IS NULL OR decided_by <> requested_by)
);
-- Bir sonuç için aynı anda yalnızca bir bekleyen talep olabilir.
CREATE UNIQUE INDEX grade_corrections_one_pending ON grade_corrections (result_id) WHERE status = 'pending';
CREATE INDEX grade_corrections_status_idx ON grade_corrections (status, id);

-- Not geçmişi: her değişiklik eski/yeni değerle, değiştirilemez biçimde saklanır.
CREATE TABLE grade_history (
    id          BIGSERIAL PRIMARY KEY,
    result_id   BIGINT NOT NULL REFERENCES course_results(id),
    old_letter  TEXT   NOT NULL,
    new_letter  TEXT   NOT NULL,
    requested_by BIGINT NOT NULL REFERENCES users(id),
    approved_by  BIGINT NOT NULL REFERENCES users(id),
    reason      TEXT   NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX grade_history_result_idx ON grade_history (result_id, id);
CREATE TRIGGER grade_history_no_update_delete
    BEFORE UPDATE OR DELETE ON grade_history
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
CREATE TRIGGER grade_history_no_truncate
    BEFORE TRUNCATE ON grade_history
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_immutable();

-- GANO yeniden hesaplama kuyruğu. Satır = "bu öğrencinin ortalaması eskimiş olabilir".
-- İş idempotenttir: her seferinde kaynak veriden sıfırdan hesaplanır.
CREATE TABLE gano_jobs (
    student_id  BIGINT PRIMARY KEY REFERENCES students(id) ON DELETE CASCADE,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE gano_jobs;
DROP TABLE grade_history;
DROP TABLE grade_corrections;
ALTER TABLE course_results DROP COLUMN enrollment_id;
DROP TABLE component_scores;
DROP TABLE grade_components;
ALTER TABLE students DROP COLUMN gano_updated_at, DROP COLUMN gano;
ALTER TABLE programs DROP COLUMN retake_policy;
ALTER TABLE sections DROP COLUMN published_at, DROP COLUMN grading_method;
