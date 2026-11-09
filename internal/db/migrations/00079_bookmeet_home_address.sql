-- +goose Up
-- Where the host travels from when no earlier in-person meeting gives a starting point.
ALTER TABLE users ADD COLUMN home_address TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE users DROP COLUMN home_address;
