ALTER TABLE channels ADD COLUMN voice_capacity integer NOT NULL DEFAULT 8 CHECK (voice_capacity BETWEEN 2 AND 32);
