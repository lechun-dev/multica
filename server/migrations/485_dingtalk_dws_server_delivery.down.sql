ALTER TABLE dingtalk_personal_message
    ALTER COLUMN expires_at SET DEFAULT (now() + interval '24 hours');

DROP TABLE IF EXISTS dingtalk_dws_oauth_state;
DROP TABLE IF EXISTS dingtalk_dws_credential;
