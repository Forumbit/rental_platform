ALTER TABLE users ADD COLUMN email_verified_at TIMESTAMPTZ;

-- Accounts created before public registration, including the seeded owner,
-- retain access. All future registrations start with an unverified address.
UPDATE users SET email_verified_at = created_at;

CREATE TABLE email_verifications (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    id VARCHAR(32) NOT NULL UNIQUE CHECK (id ~ '^[0-9a-f]{32}$'),
    token_hash BYTEA NOT NULL UNIQUE CHECK (OCTET_LENGTH(token_hash) = 32),
    code_hash BYTEA NOT NULL CHECK (OCTET_LENGTH(code_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    attempts SMALLINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX email_verifications_expires_at_idx ON email_verifications (expires_at);
