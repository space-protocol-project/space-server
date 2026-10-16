ALTER TABLE contents ADD COLUMN reactions jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE contents ADD CHECK (jsonb_typeof(reactions)='array');
