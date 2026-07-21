-- +goose Up
ALTER TABLE generation_jobs ADD COLUMN dismissed_at timestamptz;
ALTER TABLE generation_staged_outputs ADD COLUMN blur_data_url text;

ALTER TABLE assets ADD CONSTRAINT assets_blur_data_url_size_check
    CHECK (blur_data_url IS NULL OR char_length(blur_data_url) <= 4096);
ALTER TABLE generation_staged_outputs ADD CONSTRAINT staged_blur_data_url_size_check
    CHECK (blur_data_url IS NULL OR char_length(blur_data_url) <= 4096);

UPDATE generation_jobs
SET error_code='CONTENT_POLICY_REJECTED',
    error_message='图片可能触发安全策略，请调整描述',
    updated_at=now()
WHERE error_message ~* '(sensitive information|sensitive content|content policy|content moderated|request moderated|safety policy)';

-- +goose Down
ALTER TABLE generation_staged_outputs DROP CONSTRAINT IF EXISTS staged_blur_data_url_size_check;
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_blur_data_url_size_check;
ALTER TABLE generation_staged_outputs DROP COLUMN IF EXISTS blur_data_url;
ALTER TABLE generation_jobs DROP COLUMN IF EXISTS dismissed_at;
