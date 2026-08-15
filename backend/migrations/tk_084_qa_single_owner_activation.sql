-- Extend the existing append-only QA lifecycle ledger for the final
-- maintenance-owner handoff. No parallel activation table is introduced.
ALTER TABLE qa_lifecycle_receipts
    DROP CONSTRAINT IF EXISTS qa_lifecycle_receipts_phase_check;

ALTER TABLE qa_lifecycle_receipts
    ADD CONSTRAINT qa_lifecycle_receipts_phase_check
    CHECK (phase IN ('activate', 'finalize', 'single_owner_activate'));
