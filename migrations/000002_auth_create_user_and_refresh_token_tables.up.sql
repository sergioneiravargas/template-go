CREATE TABLE auth_user (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    given_name TEXT NOT NULL DEFAULT '',
    family_name TEXT NOT NULL DEFAULT '',
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX auth_user_email_key ON auth_user (LOWER(email));

CREATE TABLE auth_refresh_token (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES auth_user (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    family_id UUID NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    replaced_by UUID
);

CREATE UNIQUE INDEX auth_refresh_token_token_hash_key ON auth_refresh_token (token_hash);
CREATE INDEX auth_refresh_token_user_id_idx ON auth_refresh_token (user_id);
CREATE INDEX auth_refresh_token_family_id_idx ON auth_refresh_token (family_id);
