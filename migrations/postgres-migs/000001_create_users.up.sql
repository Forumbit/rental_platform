CREATE TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email VARCHAR(255) NOT NULL
        CHECK (email = BTRIM(email) AND email <> ''),
    password_hash TEXT NOT NULL,
    full_name VARCHAR(150) NOT NULL
        CHECK (BTRIM(full_name) <> ''),
    phone VARCHAR(30),
    role VARCHAR(20) NOT NULL DEFAULT 'user'
        CHECK (role IN ('user', 'admin')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX users_email_unique_idx ON users (LOWER(email));