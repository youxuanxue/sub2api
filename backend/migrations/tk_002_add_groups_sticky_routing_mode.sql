-- TokenKey: add per-group sticky routing strategy column.
-- See docs/approved/sticky-routing.md §3.1.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'groups'
          AND column_name = 'sticky_routing_mode'
    ) THEN
        ALTER TABLE groups
            ADD COLUMN sticky_routing_mode VARCHAR(16) NOT NULL DEFAULT 'auto';

        ALTER TABLE groups
            ADD CONSTRAINT groups_sticky_routing_mode_check
            CHECK (sticky_routing_mode IN ('auto', 'passthrough', 'off'));

        COMMENT ON COLUMN groups.sticky_routing_mode IS
            'Sticky routing strategy for upstream prompt cache hits: auto | passthrough | off';
    END IF;
END $$;
