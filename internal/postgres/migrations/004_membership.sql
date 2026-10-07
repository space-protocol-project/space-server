CREATE TABLE memberships (
    principal_id text PRIMARY KEY REFERENCES principals(id),
    role text NOT NULL CHECK (role IN ('admin','member','reader')),
    blocked boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1
);
INSERT INTO memberships(principal_id,role) SELECT id,'member' FROM principals;
CREATE TABLE invitations (
    id text PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
    role text NOT NULL CHECK (role IN ('member','reader')),
    expires_at timestamptz NOT NULL,
    max_uses integer NOT NULL CHECK (max_uses BETWEEN 1 AND 100),
    uses integer NOT NULL DEFAULT 0 CHECK (uses>=0 AND uses<=max_uses),
    revoked boolean NOT NULL DEFAULT false,
    created_by text NOT NULL REFERENCES principals(id)
);
CREATE TABLE invitation_redemptions (
    invite_id text NOT NULL REFERENCES invitations(id),
    principal_id text NOT NULL REFERENCES principals(id),
    PRIMARY KEY(invite_id,principal_id)
);
