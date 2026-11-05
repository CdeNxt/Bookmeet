-- +goose Up
ALTER TABLE server_settings ADD COLUMN public_theme TEXT NOT NULL DEFAULT 'auto';
ALTER TABLE server_settings ADD COLUMN public_copy TEXT NOT NULL DEFAULT '{}';
-- +goose Down
ALTER TABLE server_settings DROP COLUMN public_copy;
ALTER TABLE server_settings DROP COLUMN public_theme;
