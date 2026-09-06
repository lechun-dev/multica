-- 2026-09-06 coder(lq): Track successful interactive logins separately from
-- organization-directory imports so authorization browsers can show whether
-- a synchronized employee has actually signed in to Multica.
CREATE TABLE projectauth_user_logins (
    user_id UUID NOT NULL PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
    last_logged_in_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
