# Сервер: первый локальный прототип

Реализованы Go-сервер, внутренний gRPC и HTTP/JSON через сгенерированный grpc-gateway. Обе точки входа используют один сервис. Это техническая проверка ADR-015; стратегическое решение ADR-013 пока открыто.

## Запуск

Нужен Go 1.24 или новее. Из каталога `server`:

```powershell
$env:SPACE_DATABASE_URL = 'postgres://space:YOUR_PASSWORD@127.0.0.1:5432/space?sslmode=disable'
go run ./cmd/space-server
```

Адрес: `http://127.0.0.1:8080`. Другой локальный порт: `go run ./cmd/space-server -http 127.0.0.1:8081`. Внешний интерфейс отклоняется. gRPC слушает `127.0.0.1:9090` для нативного Flutter-клиента и внутреннего gateway. Изменение: `-grpc 127.0.0.1:9091`. Discovery содержит `grpc_endpoint`; оба транспорта остаются локальными без TLS. [Flutter-клиент](../client/README.md) использует существующий interceptor.

PostgreSQL можно поднять через [локальный Compose](../deploy/README.md). Пароль в URL должен быть URL-encoded. Без `SPACE_DATABASE_URL` обычный запуск останавливается; при недоступности базы автоматического перехода в память нет. Для демонстрации без базы: `go run ./cmd/space-server -demo`.

Миграции `001_initial.sql`, `002_events_auth.sql` `003_space_settings.sql` и `004_membership.sql` встроены в бинарный файл. На старте они выполняются транзакционно под advisory lock; применённые контрольные суммы сохраняются. Изменённая миграция или более новая версия схемы отклоняется. Изменения схемы следует добавлять новой миграцией и расширять runner; сейчас он поддерживает версии 1–4. Upgrade сохраняет старые сообщения и создаёт для них события.

В PostgreSQL хранятся `server_state`, `channels`, `contents` и `schema_migrations`. `server_id` и seed серверного Ed25519-ключа создаются один раз. Потерянная identity в уже инициализированной базе вызывает отказ, а не тихую замену. Discovery публикует только публичный ключ в base64url без padding; подпись discovery/manifest пока не реализована.

## Проверка вручную

Следующие простые примеры без токена работают в `-demo`. В постоянном режиме Content/Sync API требуют `Authorization: Bearer TOKEN`. Discovery и manifest остаются публичными. Для полного сценария с PostgreSQL запустите сервер, затем во втором терминале из каталога `server`:

```powershell
go run ./cmd/space-demo
```

Клиент создаёт временные root/device keys, регистрирует grant, входит по подписи, отправляет сообщение, проверяет повтор и читает события, затем отзывает grant и проверяет HTTP 401. Ключи и токен не сохраняются и не выводятся. Это тестовый клиент, не vault или recovery-инструмент. Указание другого локального origin: `go run ./cmd/space-demo -origin http://127.0.0.1:8081`.

```powershell
Invoke-RestMethod http://127.0.0.1:8080/.well-known/space-protocol
Invoke-RestMethod http://127.0.0.1:8080/api/v1/manifest
$message = @{ text = 'Привет из Space'; idempotencyKey = 'example-1' } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/api/v1/channels/general/content -ContentType 'application/json; charset=utf-8' -Body ([System.Text.Encoding]::UTF8.GetBytes($message))
Invoke-RestMethod http://127.0.0.1:8080/api/v1/channels/general/content
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/channels/general/content?after=message-1'
```

Один канал `general` и одно представление `chat`. Повтор ключа с тем же текстом возвращает прежнее сообщение; другой текст даёт gRPC `AlreadyExists` / HTTP 409. Неизвестный канал даёт 404. Список возвращает до 100 сообщений и `nextCursor`; продолжение — через `after`. Неизвестный курсор отклоняется.

Текст ограничен 4096 байтами, ключ — 128 байтами, запрос — 16 KiB, хранилище — 1000 сообщениями. Это ограничения прототипа.

## Проверки

```powershell
go test ./...
go vet ./...
```

Интеграционные тесты проверяют общий state HTTP/gRPC, ProtoJSON с русским текстом, идемпотентность, конфликт, ошибки, discovery/manifest, курсор и параллельные повторы.

Для проверки PostgreSQL задайте `SPACE_TEST_DATABASE_URL` с тестовой PostgreSQL URL и запустите `go test ./...`. Тест создаёт случайную отдельную схему и удаляет только её. Требуется право создавать схемы. Без переменной тесты базы явно пропускаются; в CI она обязательна и PostgreSQL запускается отдельным service container. Проверяются конкурентная инициализация, reopen pool с прежней identity/сообщениями/идемпотентностью, параллельная запись, страницы, изменённая миграция и потеря identity.

## Границы реализации

В режиме PostgreSQL сообщения, ключи идемпотентности, server ID, серверный ключ, grants, token hashes и события сохраняются после перезапуска. В режиме `-demo` данные исчезают и ID остаётся условным `local-prototype`; вход и events API там отключены. В постоянном режиме actor берётся из проверенной сессии, ключ идемпотентности имеет область `(channel, principal, key)`.

Работают одноразовые registration/login/revoke challenges, root/device Ed25519-подписи и opaque access tokens на 10 минут. Подробный формат и ограничения описаны в [ADR-016](../docs/adr/016-local-auth-events.md). Open registration даёт доступ только к одному общему чату; реализованы роли и приглашения для пространства, но ACL отдельных каналов, refresh, passkeys и recovery ещё предстоят.

`GET /api/v1/channels/general/events?after=event-1` возвращает до 100 событий и `nextCursor`. Событие записывается атомарно с сообщением; retry не добавляет событие. `Subscribe` передаёт replay/live events через gRPC и gateway endpoint `/api/v1/channels/general/events/subscribe`. Heartbeat, deadlines, revoke и ограничения описаны в [ADR-018](../docs/adr/018-event-stream.md). Сервер пока читает log каждые 500 ms; это не LISTEN/NOTIFY/outbox worker.

Серверный seed хранится в базе без отдельного шифрования: доступ к базе или её backup даёт серверный ключ. Это не пользовательский recovery seed. До production нужен отдельный secret/key backend и проверка ротации/восстановления. Ограничьте доступ к базе и резервным копиям.

Далее: ACL отдельных разделов; полноценный локальный vault и refresh; event-driven wakeup и snapshot watermark; панель настройки и веб-клиент; полный Docker deployment. Сейчас Compose запускает только PostgreSQL, Go-сервер работает на хосте. Первый работающий срез веб-панели встроен по /space.

## Панель владельца

Встроенная панель доступна по `/space`: первоначальный код, проверка owner и space.manage, названия, включение чата и регистрация. [Локальный запуск, SSH-туннель и ограничения ключей](../admin-web/README.md). AdminService входит в общий .proto; Logout удаляет текущую сессию. Host/Origin сверяются с -origin; POST/PATCH требуют application/json. Текущий профиль не поддерживает публичный HTTPS.