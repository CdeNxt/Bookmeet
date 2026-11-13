-- +goose Up
-- The personal booking assistant's switch, and the Telegram bot the owner talks to it through.
-- The bot token and the webhook secret are stored encrypted, like the other keys in this row.
ALTER TABLE server_settings ADD COLUMN assistant_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_settings ADD COLUMN telegram_bot_token_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE server_settings ADD COLUMN telegram_bot_username TEXT NOT NULL DEFAULT '';
ALTER TABLE server_settings ADD COLUMN telegram_webhook_secret_enc TEXT NOT NULL DEFAULT '';

-- Which Telegram chat belongs to which account. A chat that is not in here is a stranger,
-- and the bot does not answer strangers.
CREATE TABLE telegram_links (
    user_id       TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    chat_id       INTEGER NOT NULL UNIQUE,
    telegram_name TEXT NOT NULL DEFAULT '',
    linked_at     TEXT NOT NULL
);

-- One-time codes that tie a chat to an account: made in Settings, spent by opening the bot
-- with the code. Only the SHA-256 is stored.
CREATE TABLE telegram_link_codes (
    code_hash  TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE telegram_link_codes;
DROP TABLE telegram_links;
ALTER TABLE server_settings DROP COLUMN telegram_webhook_secret_enc;
ALTER TABLE server_settings DROP COLUMN telegram_bot_username;
ALTER TABLE server_settings DROP COLUMN telegram_bot_token_enc;
ALTER TABLE server_settings DROP COLUMN assistant_enabled;
