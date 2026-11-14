-- +goose Up
-- Liquid Glass level of the panels on the public booking pages: 0 = clear, 100 = tinted.
-- 50 is the middle of the slider in Settings > Branding.
-- Staging ran this as 00088 before the assistant's exceptions migration existed, so it keeps
-- that number and the exceptions migration follows it as 00089.
ALTER TABLE server_settings ADD COLUMN glass_tint INTEGER NOT NULL DEFAULT 50;
-- +goose Down
ALTER TABLE server_settings DROP COLUMN glass_tint;
