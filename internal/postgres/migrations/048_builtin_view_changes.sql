-- Служебные изменения прежнего view остаются возможны. Внешний ключ
-- проверяется при COMMIT, когда AFTER-триггер уже заменил привязку.
ALTER TABLE builtin_extension_instances
    DROP CONSTRAINT builtin_extension_instances_channel_id_view_id_fkey,
    ADD CONSTRAINT builtin_extension_instances_channel_id_view_id_fkey
        FOREIGN KEY (channel_id, view_id) REFERENCES channels(id, view_type)
        ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;

CREATE OR REPLACE FUNCTION register_builtin_channel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM builtin_extension_instances WHERE channel_id=OLD.id AND view_id=OLD.view_type;
    END IF;
    INSERT INTO builtin_extension_instances(id, installation_id, channel_id, view_id)
        VALUES ('builtin:' || NEW.id || ':' || NEW.view_type, 'builtin:' || NEW.view_type, NEW.id, NEW.view_type);
    RETURN NEW;
END;
$$;
CREATE TRIGGER channel_builtin_view_change AFTER UPDATE OF view_type ON channels
    FOR EACH ROW WHEN (OLD.view_type IS DISTINCT FROM NEW.view_type)
    EXECUTE FUNCTION register_builtin_channel();
