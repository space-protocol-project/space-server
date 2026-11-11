CREATE TABLE builtin_extension_installations (
    id text PRIMARY KEY,
    plugin_id text NOT NULL UNIQUE,
    view_type text NOT NULL UNIQUE,
    version text NOT NULL DEFAULT 'legacy.p2' CHECK (version = 'legacy.p2'),
    CHECK ((id, plugin_id, view_type) IN (
        ('builtin:chat', 'space.chat', 'chat'),
        ('builtin:voice_room', 'space.rooms', 'voice_room'),
        ('builtin:forum', 'space.discussions', 'forum'),
        ('builtin:feed', 'space.feed', 'feed'))),
    UNIQUE(id, view_type)
);
INSERT INTO builtin_extension_installations(id, plugin_id, view_type) VALUES
    ('builtin:chat', 'space.chat', 'chat'),
    ('builtin:voice_room', 'space.rooms', 'voice_room'),
    ('builtin:forum', 'space.discussions', 'forum'),
    ('builtin:feed', 'space.feed', 'feed');

ALTER TABLE channels ADD CONSTRAINT channels_builtin_view UNIQUE(id, view_type);
CREATE TABLE builtin_extension_instances (
    id text PRIMARY KEY,
    installation_id text NOT NULL,
    channel_id text NOT NULL,
    view_id text NOT NULL,
    UNIQUE(channel_id, view_id),
    CHECK (id = 'builtin:' || channel_id || ':' || view_id),
    FOREIGN KEY (installation_id, view_id) REFERENCES builtin_extension_installations(id, view_type),
    FOREIGN KEY (channel_id, view_id) REFERENCES channels(id, view_type) ON DELETE CASCADE
);
INSERT INTO builtin_extension_instances(id, installation_id, channel_id, view_id)
    SELECT 'builtin:' || id || ':' || view_type, 'builtin:' || view_type, id, view_type FROM channels;

-- Старый CreateChannel остаётся единственным писателем пользовательских данных.
-- Метаданные появляются в той же транзакции, включая старые внутренние вызовы.
CREATE FUNCTION register_builtin_channel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO builtin_extension_instances(id, installation_id, channel_id, view_id)
        VALUES ('builtin:' || NEW.id || ':' || NEW.view_type, 'builtin:' || NEW.view_type, NEW.id, NEW.view_type);
    RETURN NEW;
END;
$$;
CREATE TRIGGER channel_builtin_registration AFTER INSERT ON channels
    FOR EACH ROW EXECUTE FUNCTION register_builtin_channel();
