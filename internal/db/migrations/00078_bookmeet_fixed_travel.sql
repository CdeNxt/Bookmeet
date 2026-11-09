-- +goose Up
ALTER TABLE event_types ADD COLUMN in_person_before_minutes INTEGER NOT NULL DEFAULT 0 CHECK (in_person_before_minutes BETWEEN 0 AND 360);
ALTER TABLE event_types ADD COLUMN in_person_after_minutes INTEGER NOT NULL DEFAULT 0 CHECK (in_person_after_minutes BETWEEN 0 AND 360);
ALTER TABLE event_types ADD COLUMN fixed_drive_minutes INTEGER NOT NULL DEFAULT 0 CHECK (fixed_drive_minutes BETWEEN 0 AND 360);
ALTER TABLE bookings ADD COLUMN reserved_before_minutes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bookings ADD COLUMN reserved_after_minutes INTEGER NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE bookings DROP COLUMN reserved_after_minutes;
ALTER TABLE bookings DROP COLUMN reserved_before_minutes;
ALTER TABLE event_types DROP COLUMN fixed_drive_minutes;
ALTER TABLE event_types DROP COLUMN in_person_after_minutes;
ALTER TABLE event_types DROP COLUMN in_person_before_minutes;
