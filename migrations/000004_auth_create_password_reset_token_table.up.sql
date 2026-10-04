CREATE TABLE auth_password_reset_token (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES auth_user (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX auth_password_reset_token_token_hash_key ON auth_password_reset_token (token_hash);
CREATE INDEX auth_password_reset_token_user_id_idx ON auth_password_reset_token (user_id);
