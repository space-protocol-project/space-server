-- Дата старых записей неизвестна: они не участвуют в автоматической очистке.
ALTER TABLE remote_backup_versions ADD COLUMN created_at timestamptz;
ALTER TABLE remote_backup_versions ADD COLUMN confirmed boolean NOT NULL DEFAULT false;
