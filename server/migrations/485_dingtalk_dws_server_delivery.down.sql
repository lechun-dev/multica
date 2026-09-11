DROP INDEX CONCURRENTLY IF EXISTS idx_dingtalk_personal_message_server_claim;

ALTER TABLE dingtalk_personal_message
    ALTER COLUMN expires_at SET DEFAULT (now() + interval '24 hours');

DROP TABLE IF EXISTS dingtalk_dws_oauth_state;
DROP TABLE IF EXISTS dingtalk_dws_credential;
