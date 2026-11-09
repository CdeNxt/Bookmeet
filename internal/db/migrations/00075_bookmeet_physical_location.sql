-- +goose Up
ALTER TABLE event_types ADD COLUMN allow_in_person INTEGER NOT NULL DEFAULT 0;
ALTER TABLE event_types ADD COLUMN in_person_video INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bookings ADD COLUMN physical_location TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE bookings DROP COLUMN physical_location;
ALTER TABLE event_types DROP COLUMN in_person_video;
ALTER TABLE event_types DROP COLUMN allow_in_person;
