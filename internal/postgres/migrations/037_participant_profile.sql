ALTER TABLE memberships
  ADD COLUMN profile_avatar bytea,
  ADD COLUMN profile_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE memberships
  ADD CONSTRAINT member_avatar_size CHECK (octet_length(profile_avatar) <= 8192),
  ADD CONSTRAINT member_profile_revision CHECK (profile_revision >= 0);
