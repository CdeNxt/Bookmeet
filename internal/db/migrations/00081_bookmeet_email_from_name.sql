-- +goose Up
-- The sender name was seeded as the upstream product name. Rename only that untouched default;
-- a name the operator typed is left alone.
UPDATE server_settings SET email_from_name = 'Bookmeet' WHERE email_from_name = 'Calnode';
-- +goose Down
UPDATE server_settings SET email_from_name = 'Calnode' WHERE email_from_name = 'Bookmeet';
