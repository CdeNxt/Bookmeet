-- +goose Up
ALTER TABLE server_settings ADD COLUMN group_times INTEGER NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE server_settings DROP COLUMN group_times;
