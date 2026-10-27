-- Удаляется только отдельная роль свидетеля, не хранившая контейнеров.
-- Копии и атомарно сохранённые головы сервера пространства остаются.
DELETE FROM remote_backup_slots WHERE witness;
ALTER TABLE remote_backup_slots DROP COLUMN witness;
