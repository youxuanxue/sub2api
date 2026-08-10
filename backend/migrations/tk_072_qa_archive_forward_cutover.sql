-- TK: immutable QA Phase 2 forward cutover (design-qa-phase2-archive-closeout.md).
-- bluegreen-safe-destructive-ok: expand-only column, constraint, index, function, and
-- trigger; old application versions ignore the new false-defaulted control marker.
ALTER TABLE qa_archive_shards
    ADD COLUMN IF NOT EXISTS forward_cutover boolean NOT NULL DEFAULT false;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'qa_archive_shards'::regclass
          AND conname = 'qa_archive_shards_forward_cutover_valid'
    ) THEN
        ALTER TABLE qa_archive_shards
            ADD CONSTRAINT qa_archive_shards_forward_cutover_valid
            CHECK (
                NOT forward_cutover
                OR (state = 'committed' AND restore_verified_at IS NOT NULL)
            ) NOT VALID;
    END IF;
END
$$;

ALTER TABLE qa_archive_shards
    VALIDATE CONSTRAINT qa_archive_shards_forward_cutover_valid;

CREATE UNIQUE INDEX IF NOT EXISTS idx_qa_archive_shards_one_forward_cutover
    ON qa_archive_shards (forward_cutover)
    WHERE forward_cutover;

CREATE OR REPLACE FUNCTION prevent_qa_archive_forward_cutover_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.forward_cutover THEN
            RAISE EXCEPTION 'forward cutover cannot be deleted';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.forward_cutover AND (
        NOT NEW.forward_cutover
        OR NEW.window_start IS DISTINCT FROM OLD.window_start
        OR NEW.window_end IS DISTINCT FROM OLD.window_end
        OR NEW.generation IS DISTINCT FROM OLD.generation
    ) THEN
        RAISE EXCEPTION 'forward cutover cannot be moved or unset';
    END IF;
    RETURN NEW;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_trigger
        WHERE tgrelid = 'qa_archive_shards'::regclass
          AND tgname = 'qa_archive_shards_forward_cutover_immutable'
          AND NOT tgisinternal
    ) THEN
        CREATE TRIGGER qa_archive_shards_forward_cutover_immutable
            BEFORE UPDATE OF forward_cutover, window_start, window_end, generation OR DELETE
            ON qa_archive_shards
            FOR EACH ROW
            EXECUTE FUNCTION prevent_qa_archive_forward_cutover_change();
    END IF;
END
$$;
