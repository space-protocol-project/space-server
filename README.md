# Space Server

Сервер Space и домашний runtime. Один и тот же Go-сервер предоставляет discovery, gRPC/HTTP API, PostgreSQL, чат, каналы и ACL, участников, приглашения, устройства, recovery и сопряжение.

- [Общее Flutter-приложение](https://github.com/space-protocol-project/space-app).
- [Протокол и архитектура](https://github.com/space-protocol-project/space-protocol).
- [Домашний запуск из приложения](docs/HOME-SERVER.md).
- [Выпуски сервера и runtime](https://github.com/space-protocol-project/space-server/releases).

## Сервер и домашний runtime

Сервер — сама программа. Домашний runtime — комплект этой программы с PostgreSQL для автоматической установки и запуска кнопкой «Выйти в эфир» в Space. Отдельной серверной реализации или браузерной админки нет. Повторный запуск сохраняет прежнюю базу, server ID, сертификат, владельца и участников; «Отключиться» останавливает процессы с сохранением данных.

## Запуск для разработки

Нужны Go 1.24+ и отдельная PostgreSQL. Из корня этого репозитория:

```powershell
$env:SPACE_DATABASE_URL = 'postgres://space:YOUR_PASSWORD@127.0.0.1:5432/space?sslmode=disable'
go run ./cmd/space-server
```

Обычный режим использует loopback HTTP 127.0.0.1:8080 и gRPC 127.0.0.1:9090. Изменение портов: `-http` и `-grpc`. Временный режим `-demo` не предназначен для настоящего домашнего пространства.

Домашний режим использует `-home-data`, `-home-runtime`, `-public-ip`, `-https` и `-managed`; приложение задаёт их автоматически. По умолчанию HTTPS и HTTP/2 gRPC используют TCP 8443. PostgreSQL и внутренние входы остаются на loopback. Сертификат самоподписанный, его отпечаток закрепляется в приложении и передаётся в ссылке приглашения. Публичный вход закрыт до локального назначения владельца.

Миграции встроены в бинарник и проверяются по checksum. Разделение репозиториев не изменяет SQL-миграции, формат базы или identity сервера. Не удаляйте кластер для обновления бинарника.

## Проверки и релизы

Первый экспериментальный выпуск: [v0.1.0](https://github.com/space-protocol-project/space-server/releases/tag/v0.1.0). Полный [CI 38065087775](https://github.com/space-protocol-project/space-server/actions/runs/38065087775) прошёл на трёх платформах и опубликовал архивы с описанием из Changelog. Установка этих публичных архивов самим Flutter-приложением дополнительно проверена в [CI приложения 38067026461](https://github.com/space-protocol-project/space-app/actions/runs/38067026461).

`go test ./...` и `go vet ./...` выполняются из корня. Полные PostgreSQL и сквозные Flutter/Go/WebCrypto-проверки выполняются в CI. Flutter берётся из `space-app`; локальный Docker для этой работы не запускается.

Теги `vX.Y.Z` собирают Windows x64, macOS arm64 и Linux x64, проверяют перезапуск и приглашённого участника через TLS, затем публикуют архивы и SHA-256. Описание берётся из соответствующего раздела [Changelog.md](Changelog.md). Без полного успешного CI публикации нет. Обычные и ручные сборки сохраняют артефакты.

Контракт `.proto` находится в `space-protocol`. Go bindings коммитятся в `gen/`. Из этого репозитория генерация выполняется `buf generate ../space-protocol/protocol/proto --template buf.gen.yaml` с установленными генераторами, версии — в руководстве генерации протокола.

Проект экспериментальный. Интернет-доступ через домашний роутер, установка и защищённое хранилище macOS/Linux, аварийное завершение, резервные копии и смена публичного IP требуют отдельных проверок или следующих этапов.

В main/Unreleased добавлено серверное время Content.created_at_unix_ms. Миграция 010 сохраняет неизвестное время старой истории; создание, повтор, история и события возвращают одно сохранённое значение. [CI 38167063307](https://github.com/space-protocol-project/space-server/actions/runs/38167063307) подтвердил PostgreSQL и совместимость Dart/Go для 55a6a53. Выпуск v0.1.0 этим изменением не заменяется.

## Прототип ConnectRPC

Флаг `-connect-rpc` включает только GetManifest, CreateChallenge, CompleteChallenge и Subscribe на маршрутах `/space.v1.…/…`. По умолчанию флаг выключен. Вызовы передаются в существующий gRPC-сервер, который проверяет токены, подписи и права. grpc-gateway и штатный Flutter-клиент сохранены. Размер входящего тела и распакованного сообщения ограничен 16 КиБ. Отмена Connect-потока отменяет внутренний gRPC; запись потока имеет возобновляемый таймаут 20 секунд.

Генерация: `buf generate ../space-protocol/protocol/proto --template buf.connect.gen.yaml`. Connect-Go и генератор закреплены на 1.19.2; новые версии 1.20/1.21 требуют Go 1.25. Прямые Connect-обработчики, HTTP-аутентификация до декодирования и проверка TLS pinning из Dart остаются следующими срезами; закреплённый домашний выпуск v0.1.0 не изменяется.

Приёмка первого Connect-среза: [CI 38174641293](https://github.com/space-protocol-project/space-server/actions/runs/38174641293) успешен для 6e59209: PostgreSQL/race, Connect Dart/Go Protobuf/JSON, подписанный вход, событие и отзыв, прежний gRPC и нативный vault Windows. HTTP-аутентификация до декодирования при прямом переносе и домашний TLS pinning в Dart остаются следующими срезами.
