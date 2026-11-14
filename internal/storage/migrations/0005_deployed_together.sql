-- Deployed together (plan section 8.7, rule 2): the release ID of the release
-- that shares this one's Rollout and Verify evidence. deployed_with holds a
-- UUID and requires the other release to exist; a projection only knows
-- release IDs, and either side can be projected first.
ALTER TABLE releases ADD COLUMN deployed_with_release TEXT;
-- Why a config release bypassed CI, such as a hand edit that moved the pods
-- to another release ID or digest.
ALTER TABLE releases ADD COLUMN flag TEXT;

CREATE INDEX releases_deployed_with ON releases (deployed_with_release) WHERE deployed_with_release IS NOT NULL;
