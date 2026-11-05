-- +goose Up
ALTER TABLE server_settings ADD COLUMN booking_effect TEXT NOT NULL DEFAULT 'both';
ALTER TABLE server_settings ADD COLUMN cancel_effect TEXT NOT NULL DEFAULT 'sad';
ALTER TABLE server_settings ADD COLUMN hide_attribution INTEGER NOT NULL DEFAULT 1;
-- +goose Down
ALTER TABLE server_settings DROP COLUMN hide_attribution;
ALTER TABLE server_settings DROP COLUMN cancel_effect;
ALTER TABLE server_settings DROP COLUMN booking_effect;
