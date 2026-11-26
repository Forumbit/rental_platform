-- published_at is now managed by the server: it is set only while a listing
-- is published. Client-supplied values are normalized to that rule.
UPDATE listings SET published_at = CASE
    WHEN status = 'published' THEN LEAST(COALESCE(published_at, updated_at), NOW())
END;

ALTER TABLE listings ADD CONSTRAINT listings_published_at_status_check
    CHECK ((status = 'published') = (published_at IS NOT NULL));

-- Keyset pagination: public feed and per-owner lists, both ordered by id DESC.
DROP INDEX listings_owner_id_idx;
CREATE INDEX listings_owner_id_idx ON listings (owner_id, id DESC);
CREATE INDEX listings_published_idx ON listings (id DESC) WHERE status = 'published';

-- Periodic cleanup of revoked refresh sessions.
CREATE INDEX refresh_sessions_revoked_at_idx ON refresh_sessions (revoked_at)
    WHERE revoked_at IS NOT NULL;
