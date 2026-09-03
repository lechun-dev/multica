-- 2026-08-31 coder(lq): Drop only the additive authorization source. Legacy
-- tables are intentionally untouched so rollback cannot remove old access.
DROP TABLE IF EXISTS projectauth_organization_members;
DROP TABLE IF EXISTS projectauth_organizations;
DROP TABLE IF EXISTS projectauth_access_grants;
