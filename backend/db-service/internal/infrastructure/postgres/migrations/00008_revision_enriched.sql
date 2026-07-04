-- Add the 'enriched' revision action (recorded when Stage-2 enrichment fills a
-- hypothesis's rich passport fields from full document text, or archives it via
-- the quality gate). The action CHECK was created inline on the column, so
-- PostgreSQL named it hypothesis_revisions_action_check.

-- +goose Up
ALTER TABLE hypothesis_revisions DROP CONSTRAINT IF EXISTS hypothesis_revisions_action_check;
ALTER TABLE hypothesis_revisions ADD CONSTRAINT hypothesis_revisions_action_check
    CHECK (action IN ('created', 'edited', 'status_changed', 'score_override',
                      'approved', 'rejected', 'commented', 'verified', 'enriched'));

-- +goose Down
ALTER TABLE hypothesis_revisions DROP CONSTRAINT IF EXISTS hypothesis_revisions_action_check;
ALTER TABLE hypothesis_revisions ADD CONSTRAINT hypothesis_revisions_action_check
    CHECK (action IN ('created', 'edited', 'status_changed', 'score_override',
                      'approved', 'rejected', 'commented', 'verified'));
