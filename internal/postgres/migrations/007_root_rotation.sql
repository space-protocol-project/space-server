CREATE TABLE principal_roots (
 public_key bytea PRIMARY KEY CHECK (octet_length(public_key)=32),
 principal_id text NOT NULL REFERENCES principals(id),
 auth_epoch bigint NOT NULL CHECK (auth_epoch>0),
 retired_at timestamptz,
 UNIQUE(principal_id,auth_epoch)
);
INSERT INTO principal_roots(public_key,principal_id,auth_epoch)
 SELECT root_public_key,id,auth_epoch FROM principals;
CREATE TABLE root_rotations (
 id text PRIMARY KEY,
 principal_id text NOT NULL REFERENCES principals(id),
 operation_id text NOT NULL,
 transcript bytea NOT NULL,
 source_grant_id text NOT NULL REFERENCES device_grants(id),
 expires_at timestamptz NOT NULL,
 completed_at timestamptz,
 old_signature bytea,
 new_signature bytea,
 grant_id text UNIQUE REFERENCES device_grants(id),
 UNIQUE(principal_id,operation_id)
);
ALTER TABLE device_grants DROP CONSTRAINT device_grants_signature_kind_check;
ALTER TABLE device_grants ADD CHECK(signature_kind IN ('root','recovery','rotation'));
