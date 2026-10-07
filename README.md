# Сервер: первый локальный прототип

Реализованы Go-сервер, внутренний gRPC и HTTP/JSON через сгенерированный grpc-gateway. Обе точки входа используют один сервис. Это техническая проверка ADR-015; стратегическое решение ADR-013 пока открыто.

## Запуск

Нужен Go 1.24 или новее. Из каталога `server`:

```powershell
$env:SPACE_DATABASE_URL = 'postgres://space:YOUR_PASSWORD@127.0.0.1:5432/space?sslmode=disable'
go run ./cmd/space-server
```

Адрес: `http://127.0.0.1:8080`. Другой локальный порт: `go run ./cmd/space-server -http 127.0.0.1:8081`. Внешний интерфейс отклоняется. gRPC слушает случайный loopback-порт и используется gateway внутри процесса.

PostgreSQL можно поднять через [локальный Compose](../deploy/README.md). Пароль в URL должен быть URL-encoded. Без `SPACE_DATABASE_URL` обычный запуск останавливается; при недоступности базы автоматического перехода в память нет. Для демонстрации без базы: `go run ./cmd/space-server -demo`.

Миграция `001_initial.sql` встроена в бинарный файл. На старте она выполняется транзакционно под advisory lock; применённая контрольная сумма сохраняется. Изменённая миграция или более новая версия схемы отклоняется. Изменения схемы следует добавлять новой миграцией и расширять runner; сейчас он поддерживает только версию 1.

В PostgreSQL хранятся `server_state`, `channels`, `contents` и `schema_migrations`. `server_id` и seed серверного Ed25519-ключа создаются один раз. Потерянная identity в уже инициализированной базе вызывает отказ, а не тихую замену. Discovery публикует только публичный ключ в base64url без padding; подпись discovery/manifest пока не реализована.

## Проверка вручную

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

В режиме PostgreSQL сообщения, ключи идемпотентности, server ID и серверный ключ сохраняются после перезапуска. В режиме `-demo` данные исчезают и ID остаётся условным `local-prototype`. Авторов, ACL, входа и recovery пока нет. Ключ идемпотентности пока имеет область канала; после авторизации добавим actor и версию операции. Использовать только тестовые сообщения на локальной машине.

Серверный seed хранится в базе без отдельного шифрования: доступ к базе или её backup даёт серверный ключ. Это не пользовательский recovery seed. До production нужен отдельный secret/key backend и проверка ротации/восстановления. Ограничьте доступ к базе и резервным копиям.

Далее: durable event log и outbox; challenge/device grant и отзыв; Subscribe с replay; панель настройки и минимальный клиент; полный Docker deployment. Сейчас Compose запускает только PostgreSQL, Go-сервер работает на хосте. Веб-интерфейс ещё не реализован.
