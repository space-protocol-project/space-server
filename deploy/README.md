# Размещение

Здесь находится локальный Compose для PostgreSQL. Go-сервер пока запускается на хосте. Конфигурация не является готовым публичным размещением.

## Локальный запуск

Нужны Docker Desktop с Linux containers и Go. Из корня репозитория в PowerShell:

```powershell
$env:SPACE_POSTGRES_PASSWORD = 'replace-with-your-local-password'
docker compose -f deploy/compose.yaml up -d --wait
$encodedPassword = [uri]::EscapeDataString($env:SPACE_POSTGRES_PASSWORD)
$env:SPACE_DATABASE_URL = "postgres://space:${encodedPassword}@127.0.0.1:5432/space?sslmode=disable"
Set-Location server
go run ./cmd/space-server
```

Альтернатива: скопировать `.env.example` в `deploy/.env` и указать пароль там. Серверу всё равно нужна `SPACE_DATABASE_URL`. `.env` не коммитим. `sslmode=disable` допустим здесь только для локальной тестовой базы.

Порт PostgreSQL опубликован на `127.0.0.1:5432`. Если он занят, измените host port в Compose и URL. Volume `space-postgres` хранит данные. `docker compose -f deploy/compose.yaml down` останавливает контейнер без удаления volume. `down -v` уничтожает данные и идентичность: для обычного перезапуска его не используем. Изменение POSTGRES_PASSWORD не меняет пароль уже созданной базы — он задаётся при первой инициализации volume.

## Проверка сохранности

Создайте сообщение по [инструкции сервера](../server/README.md), сохраните ответ discovery. Остановите Go-сервер через Ctrl+C и запустите снова с той же URL. Сообщение, повтор ключа запроса, `server_id` и `signing_public_key` должны остаться прежними. Затем можно перезапустить PostgreSQL через `docker compose -f deploy/compose.yaml restart postgres` и повторить проверку.

Секретный серверный ключ находится в PostgreSQL и входит в backup. Сохранение одной публичной discovery-карточки не восстанавливает сервер. Backup/restore drill, контейнер приложения, веб-панель, assets и HTTPS proxy — следующие части размещения.

Исходный Docker skeleton в корневом README остаётся иллюстративным. Локальный Compose и PostgreSQL интеграционные тесты проверяются в CI; локальная проверка Compose требует установленного Docker.
