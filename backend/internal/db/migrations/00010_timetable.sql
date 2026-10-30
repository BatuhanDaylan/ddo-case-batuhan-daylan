-- +goose Up
CREATE TABLE rooms (
    id       BIGSERIAL PRIMARY KEY,
    code     TEXT    NOT NULL UNIQUE,
    capacity INTEGER NOT NULL CHECK (capacity > 0),
    kind     TEXT    NOT NULL CHECK (kind IN ('lecture', 'lab', 'amphi'))
);

-- Ders programı çözücüsü için dersin ihtiyaçları: haftalık ders saati ve derslik türü.
ALTER TABLE courses
    ADD COLUMN weekly_hours INTEGER NOT NULL DEFAULT 2 CHECK (weekly_hours BETWEEN 1 AND 8),
    ADD COLUMN room_kind    TEXT    NOT NULL DEFAULT 'lecture' CHECK (room_kind IN ('lecture', 'lab', 'amphi'));

-- Öğretim üyesinin müsait OLMADIĞI saatler (gün 1=Pazartesi, slot 0=ilk ders saati).
CREATE TABLE instructor_unavailable (
    user_id BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day     INTEGER NOT NULL CHECK (day BETWEEN 1 AND 7),
    slot    INTEGER NOT NULL CHECK (slot BETWEEN 0 AND 15),
    PRIMARY KEY (user_id, day, slot)
);

-- Programın atandığı derslik (elle girilmiş eski satırlarda boş olabilir).
ALTER TABLE section_meetings ADD COLUMN room_id BIGINT REFERENCES rooms(id);

-- +goose Down
ALTER TABLE section_meetings DROP COLUMN room_id;
DROP TABLE instructor_unavailable;
ALTER TABLE courses DROP COLUMN room_kind, DROP COLUMN weekly_hours;
DROP TABLE rooms;
