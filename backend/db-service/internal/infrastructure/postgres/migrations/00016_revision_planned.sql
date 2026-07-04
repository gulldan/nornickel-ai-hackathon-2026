-- Add the 'planned' revision action (recorded when PlanExperiment drafts a lab
-- experiment plan for a hypothesis). Without it the constraint rejected the
-- revision insert, rolling back the whole transaction — so the plan was
-- generated and then discarded, and POST /hypotheses/{id}/experiment 500'd.

-- +goose Up
ALTER TABLE hypothesis_revisions DROP CONSTRAINT IF EXISTS hypothesis_revisions_action_check;
ALTER TABLE hypothesis_revisions ADD CONSTRAINT hypothesis_revisions_action_check
    CHECK (action IN ('created', 'edited', 'status_changed', 'score_override',
                      'approved', 'rejected', 'commented', 'verified', 'enriched', 'planned'));

-- +goose Down
ALTER TABLE hypothesis_revisions DROP CONSTRAINT IF EXISTS hypothesis_revisions_action_check;
ALTER TABLE hypothesis_revisions ADD CONSTRAINT hypothesis_revisions_action_check
    CHECK (action IN ('created', 'edited', 'status_changed', 'score_override',
                      'approved', 'rejected', 'commented', 'verified', 'enriched'));
