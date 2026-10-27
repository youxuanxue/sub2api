-- Default admin account list orders by name, id. A btree partial index lets
-- PostgreSQL satisfy WHERE deleted_at IS NULL ORDER BY name, id LIMIT n
-- without sorting the whole accounts table.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_name_id_active
    ON accounts (name, id)
    WHERE deleted_at IS NULL;
