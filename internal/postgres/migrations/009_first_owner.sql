ALTER TABLE space_settings ADD COLUMN owner_claim_policy text NOT NULL DEFAULT 'manual' CHECK (owner_claim_policy IN ('manual','first_login'));
