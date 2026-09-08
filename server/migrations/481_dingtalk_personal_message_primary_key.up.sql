ALTER TABLE dingtalk_personal_message
    ADD CONSTRAINT dingtalk_personal_message_pkey
    PRIMARY KEY USING INDEX dingtalk_personal_message_id_uniq;
