-- Image files live in S3-compatible storage; only metadata is stored here.
CREATE TABLE listing_images (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    listing_id BIGINT NOT NULL REFERENCES listings(id) ON DELETE CASCADE,
    storage_key VARCHAR(64) NOT NULL UNIQUE
        CHECK (storage_key ~ '^[0-9a-f]{32}\.(jpg|png|webp)$'),
    content_type VARCHAR(20) NOT NULL
        CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    size_bytes INTEGER NOT NULL CHECK (size_bytes > 0),
    width INTEGER NOT NULL CHECK (width > 0),
    height INTEGER NOT NULL CHECK (height > 0),
    position SMALLINT NOT NULL CHECK (position >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX listing_images_listing_id_idx ON listing_images (listing_id, position, id);
