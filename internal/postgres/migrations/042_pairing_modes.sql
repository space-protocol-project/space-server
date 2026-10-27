ALTER TABLE device_pairings ADD COLUMN full_management boolean NOT NULL DEFAULT false;
ALTER TABLE device_pairings ADD CONSTRAINT pairing_full_administrative CHECK (NOT full_management OR administrative);
