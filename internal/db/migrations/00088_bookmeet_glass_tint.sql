-- +goose Up
-- Liquid Glass level of the panels on the public booking pages: 0 = clear, 100 = tinted.
-- 50 is the middle of the slider in Settings > Branding.
-- Numbered after the booking-assistant branch's migrations (00083-00087).
ALTER TABLE server_settings ADD COLUMN glass_tint INTEGER NOT NULL DEFAULT 50;
-- +goose Down
ALTER TABLE server_settings DROP COLUMN glass_tint;
