-- Config releases (plan section 8.7): the commit's subject and author from
-- Argo CD's commit metadata, and the release-id annotation of the pods a
-- rollout started, which a config release's verification expects.
ALTER TABLE releases ADD COLUMN gitops_subject TEXT;
ALTER TABLE releases ADD COLUMN gitops_author TEXT;
ALTER TABLE releases ADD COLUMN runtime_release_id TEXT;
