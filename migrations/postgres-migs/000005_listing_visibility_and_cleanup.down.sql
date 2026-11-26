DROP INDEX IF EXISTS refresh_sessions_revoked_at_idx;
DROP INDEX IF EXISTS listings_published_idx;
DROP INDEX IF EXISTS listings_owner_id_idx;
CREATE INDEX listings_owner_id_idx ON listings (owner_id);
ALTER TABLE listings DROP CONSTRAINT IF EXISTS listings_published_at_status_check;
