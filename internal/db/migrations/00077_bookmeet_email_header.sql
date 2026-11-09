-- +goose Up
ALTER TABLE server_settings ADD COLUMN email_header TEXT NOT NULL DEFAULT 'stacked' CHECK (email_header IN ('stacked', 'artwork', 'logo', 'off'));
UPDATE server_settings SET email_header = 'artwork' WHERE lower(trim(business_name)) = 'bookmeet';
-- +goose Down
ALTER TABLE server_settings DROP COLUMN email_header;
