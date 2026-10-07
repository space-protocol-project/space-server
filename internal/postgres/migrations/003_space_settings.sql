ALTER TABLE device_grants ADD COLUMN scopes text[] NOT NULL DEFAULT ARRAY['chat.read','chat.write'];
CREATE TABLE space_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    title text NOT NULL DEFAULT 'Моё пространство',
    chat_title text NOT NULL DEFAULT 'Общий чат',
    chat_enabled boolean NOT NULL DEFAULT true,
    registration_policy text NOT NULL DEFAULT 'open' CHECK(registration_policy IN ('open','closed')),
    revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
    owner_id text REFERENCES principals(id),
    setup_code_hash bytea CHECK(setup_code_hash IS NULL OR octet_length(setup_code_hash)=32),
    setup_expires_at timestamptz
);
INSERT INTO space_settings(singleton) VALUES(true);
CREATE TABLE admin_audit (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    principal_id text NOT NULL REFERENCES principals(id),
    action text NOT NULL,
    revision bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
