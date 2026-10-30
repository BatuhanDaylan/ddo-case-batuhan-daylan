-- +goose Up
-- F-3: danışman onayı ve ekle-sil haftası.
-- Koltuk kayıt anında alınır (adil sıra korunur); danışman onayı sonradan gelir. Reddedilen kayıt düşer ve
-- boşalan koltuk bekleme listesinin ilk kişisine atanır.
ALTER TABLE enrollments
    ADD COLUMN advisor_status     TEXT NOT NULL DEFAULT 'pending' CHECK (advisor_status IN ('pending', 'approved', 'rejected')),
    ADD COLUMN advisor_decided_by BIGINT REFERENCES users(id),
    ADD COLUMN advisor_decided_at TIMESTAMPTZ;
CREATE INDEX enrollments_advisor_idx ON enrollments (advisor_status, id) WHERE status <> 'dropped';

-- Bu tarihten sonra öğrenci kendi kaydını bırakamaz (ekle-sil haftası sona erer). NULL: sınır yok.
ALTER TABLE terms ADD COLUMN drop_deadline TIMESTAMPTZ;

-- F-4: sınav takvimi.
CREATE TABLE exams (
    id           BIGSERIAL PRIMARY KEY,
    section_id   BIGINT      NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL CHECK (kind IN ('midterm', 'final', 'makeup')),
    starts_at    TIMESTAMPTZ NOT NULL,
    duration_min INTEGER     NOT NULL CHECK (duration_min BETWEEN 15 AND 300),
    room_id      BIGINT REFERENCES rooms(id),
    UNIQUE (section_id, kind)
);
CREATE INDEX exams_time_idx ON exams (starts_at);

-- F-5: not itirazı. İtiraz kabul edilirse öğretim üyesi bir düzeltme talebi açar (ikinci kişi onayı gerekir).
CREATE TABLE grade_objections (
    id          BIGSERIAL PRIMARY KEY,
    result_id   BIGINT NOT NULL REFERENCES course_results(id),
    student_id  BIGINT NOT NULL REFERENCES students(id),
    reason      TEXT   NOT NULL,
    status      TEXT   NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'accepted', 'rejected')),
    response    TEXT,
    correction_id BIGINT REFERENCES grade_corrections(id),
    decided_by  BIGINT REFERENCES users(id),
    decided_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX grade_objections_one_open ON grade_objections (result_id) WHERE status = 'open';
CREATE INDEX grade_objections_student_idx ON grade_objections (student_id, id);

-- F-8: raporlar dönem ve ders bazında toplanır.
CREATE INDEX course_results_term_idx ON course_results (term_id, course_id);

-- +goose Down
DROP INDEX course_results_term_idx;
DROP TABLE grade_objections;
DROP TABLE exams;
ALTER TABLE terms DROP COLUMN drop_deadline;
DROP INDEX enrollments_advisor_idx;
ALTER TABLE enrollments DROP COLUMN advisor_decided_at, DROP COLUMN advisor_decided_by, DROP COLUMN advisor_status;
