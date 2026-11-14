-- +goose Up
-- An exception to the owner's availability, allowed for one personal link: the link offers
-- times inside its windows even where they fall outside the owner's hours, minimum notice or
-- how far ahead they take bookings. The owner's calendar and existing bookings still apply.
ALTER TABLE personal_links ADD COLUMN outside_availability INTEGER NOT NULL DEFAULT 0;

-- Whether the assistant may offer such exceptions at all (it still asks each time).
ALTER TABLE server_settings ADD COLUMN assistant_exceptions INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE server_settings DROP COLUMN assistant_exceptions;
ALTER TABLE personal_links DROP COLUMN outside_availability;
