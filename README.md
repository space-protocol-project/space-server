# Сервер: первый локальный прототип

Реализованы Go-сервер, внутренний gRPC и HTTP/JSON через сгенерированный grpc-gateway. Обе точки входа используют один сервис. Это техническая проверка ADR-015; стратегическое решение ADR-013 пока открыто.

## Запуск

Нужен Go 1.24 или новее. Из каталога `server`:

```powershell
go run ./cmd/space-server
```

Адрес: `http://127.0.0.1:8080`. Другой локальный порт: `go run ./cmd/space-server -http 127.0.0.1:8081`. Внешний интерфейс отклоняется. gRPC слушает случайный loopback-порт и используется gateway внутри процесса.

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

## Границы реализации

Сообщения и ключи идемпотентности исчезают при перезапуске. `local-prototype` — условный server ID. Авторов, ACL, подписей, входа и recovery пока нет. Использовать только тестовые сообщения на локальной машине.

Далее: PostgreSQL и migrations с persistent server identity; транзакционная идемпотентность и durable event log; challenge/device grant и отзыв; Subscribe с replay; панель настройки и минимальный клиент; проверенный Docker Compose. Веб-интерфейс и Docker deployment пока не реализованы.
