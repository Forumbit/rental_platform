CREATE TABLE listings (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,

    title VARCHAR(200) NOT NULL CHECK (BTRIM(title) <> ''),
    description TEXT NOT NULL CHECK (BTRIM(description) <> ''),
    property_type VARCHAR(20) NOT NULL
        CHECK (property_type IN ('house', 'cottage', 'dacha', 'apartment')),

    price_minor BIGINT NOT NULL CHECK (price_minor > 0),
    currency VARCHAR(3) NOT NULL DEFAULT 'RUB',
    price_period VARCHAR(20) NOT NULL DEFAULT 'day'
        CHECK (price_period IN ('day', 'month')),

    country VARCHAR(100) NOT NULL DEFAULT 'Россия',
    region VARCHAR(150),
    city VARCHAR(150) NOT NULL CHECK (BTRIM(city) <> ''),
    address TEXT,

    area_sqm NUMERIC(8, 2) CHECK (area_sqm > 0),
    bedrooms SMALLINT CHECK (bedrooms >= 0),
    bathrooms SMALLINT CHECK (bathrooms >= 0),
    max_guests SMALLINT CHECK (max_guests > 0),

    status VARCHAR(20) NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'archived')),

    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX listings_owner_id_idx ON listings (owner_id);