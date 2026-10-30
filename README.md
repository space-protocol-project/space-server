# Space Server

Исходный этап **P0 системы расширений завершён**: [отчёт B01–B10 и измерений](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P0-RESULTS.md), [TODO](https://github.com/space-protocol-project/space-protocol/blob/main/TODO.md). При проверке B07 исправлен транспортный предел SDP: вход через Connect больше не отклоняется общим ограничением 16 КиБ. Исправление проверено полным CI сервера и двумя нативными Flutter-клиентами через TURN TCP с ненулевым виртуальным аудио и восстановлением; пока находится в `main`, без нового выпуска runtime.

Начат [P1 расширений](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1.md): `experiments/extensions` — отдельный Go module с WASM-хостом wazero 1.12.0 и ограниченным ABI. Новый RPC пока не зарегистрирован в рабочем сервере. Workflow `plugin-p1.yml` проверяет compiler/interpreter на Linux, Windows и macOS; добавлен [ProcessHost с изоляцией ресурсов Linux](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-ISOLATION.md): отдельный процесс компиляции/выполнения, 256 МиБ на worker, 512 МиБ на общий хост, deadline и очистка потомков. Между вызовами кеша нет. Добавлен [Windows ProcessHost на Job Objects](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-WINDOWS.md): [создание сразу в обоих Job](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-WINDOWS-LAUNCH.md), общие квоты и остановка потомков; отдельный CI проверяет реальный отказ выделения памяти. На macOS защищённый режим пока явно недоступен: [исследование RLIMIT и Virtualization.framework](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-MACOS.md) отделяет адресное пространство, RSS и общую квоту. [Linux guest на Intel macOS](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-MACOS-VM.md) прошёл проверки WASM и cgroup; аварии launcher/VMM и два холодных повтора проверены в CI. Измерены RSS и footprint обоих процессов, но общий жёсткий бюджет VMM ещё открыт. Внутренний [протокол worker](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-WORKER.md) проверяет версию, точные поля и размеры до создания runtime. Linux ProcessHost [восстанавливает оставшиеся cgroup после аварии supervisor](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-RECOVERY.md) при новом запуске и запрещает одновременное владение одним корнем. Добавлен [экспериментальный профиль systemd](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-SERVICE.md): READY после инициализации, остановка дерева и автоматический повтор после аварии. Изоляция прав и включение в рабочий сервер остаются задачами P1.

Добавлен [единый внутренний Runtime и сквозной демонстрационный путь](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-DEMO.md): экспериментальный dispatcher → изолированный WASM → Flutter, с проверкой прав и восстановлением после отказа. Стенд запускается через `space-app/plugin-p1-demo.yml`; рабочая навигация пока не подключена. [Следующий срез P1](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-CORE.md) использует настоящие сессии и ACL ядра через отдельно регистрируемый protobuf RPC; Прежний конструктор запрещает мутации; [новый отдельно включаемый исполнитель P1](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-MUTATIONS.md) записывает данные, квитанцию и outbox одной транзакцией. Блокировки ограничены связанными строками; конкурентные parent/pairing/root rotation и независимый logout проверены в CI. Отдельный процесс проверяет откат после SIGKILL до commit и выдачу квитанции после SIGKILL/потери ответа после commit. Добавлен [внутренний прототип outbox](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-OUTBOX.md): аренда, курсор и подтверждение для одного доверенного обработчика. [Копирование и восстановление данных расширений](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-BACKUP.md) проверено на Linux/Windows/macOS; старые аренды отзываются в новой копии, подтверждённый курсор сохраняется. Рабочий сервер его не вызывает. [Конкурентные изменения прав и истечение доступа перед commit](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-ACCESS.md) проверены в CI; длительная смешанная нагрузка остаётся открытой. [Совместная авария сервера и WASM worker](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-WORKER-CRASH.md) и восстановление до/после commit проверены в Linux CI. [Отказ PostgreSQL и неопределённый COMMIT](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-DATABASE-FAILURE.md) проверены с ограниченными повторами той же команды; рабочая политика повторов клиента ещё не подключена.

[Конечные критерии закрытия P1 — E01–E07](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-EXIT.md) отделяют контракты и прототипы от рабочей интеграции P2/P3. Все семь условий пока открыты; общий бюджет macOS и платформенная приёмка остаются обязательными. Закрытие P1 не означает готовность 0.9.0. Подготовлен [кандидат правил ошибок и повторов](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS-P1-RETRY.md) с общей политикой Go/Dart; автоматическая отправка повторов рабочего клиента не подключена.

В `main` реализуются режимы сопряжения для приложения 0.8.0: полный режим
получает `device.manage`, ограниченный — рабочие разрешения выбранных пространств.
Сервер фиксирует режим до подписи, проверяет полномочия, срок и отзыв родителя,
запрещает выдачу управляющих полномочий через дочерний grant и смену root
из сопряжённой сессии. Схема PostgreSQL — **46**;
[полномочия и правила](https://github.com/space-protocol-project/space-app/blob/main/docs/PAIRING-MODES.md).
Runtime v0.7.25 уже содержит эти функции; v0.8.0 сохраняет тот же API и схему 46.

`RemoteBackupService` — функция сервера пространства: зашифрованный контейнер и его текущая голова сохраняются одной транзакцией. Приложение выбирает одно подключённое пространство; его сервер может работать самостоятельно либо запускаться через runtime. Отдельных типов серверов и внешнего свидетеля нет. Создание/отзыв требуют сессию участника/владельца, доступ к копиям использует разные capability. [Модель и восстановление](https://github.com/space-protocol-project/space-app/blob/main/docs/REMOTE-DIRECTORY-BACKUP.md).

Добавлен `DirectoryPairingService`: непрозрачная передача каталога через capability
на пять минут. PostgreSQL хранит ciphertext и хеши capability, но не ключи Noise.
Сверка кода и шифрование выполняются на двух клиентах; relay не выдаёт доступ
к пространствам. Ограничены роли, порядок, размер кадров и общий объём хранения;
одноразовый code потребляется атомарно, повтор записи допустим только с теми же байтами.
[Модель угроз и профиль Snow/Noise](https://github.com/space-protocol-project/space-app/blob/main/docs/DIRECTORY-PAIRING.md).

Планируемая архитектура: [система расширений Space](https://github.com/space-protocol-project/space-protocol/blob/main/docs/SPACE-PLUGINS.md), [TODO и milestones](https://github.com/space-protocol-project/space-protocol/blob/main/TODO.md). Сервер получает реестр пакетов и экземпляров, ограниченный WASM-хост, API хранения/событий, проверку разрешений и миграции. Чаты, Комнаты, Обсуждения и Лента сначала подключаются внутренними адаптерами с сохранением существующих API; затем прикладная логика переносится поэтапно. WebRTC/TURN, идентичность и проверка доступа остаются специализированными службами. Это план, в текущем runtime установки WASM-пакетов ещё нет.

Публичная система расширений планируется после приёмки 0.8.0 приложения с сопряжением и зашифрованными резервными копиями: 0.9 — основа, 0.10 — Обсуждения/Лента, 0.11 — Чаты/Комнаты. Runtime сохраняет собственную нумерацию и фиксирует совместимую пару при каждом выпуске; подготовка 0.8.0 открыта 27 октября 2026 года. Будущие номера схем БД заранее не назначаются.

Опубликован [runtime **v0.7.23**](https://github.com/space-protocol-project/space-server/releases/tag/v0.7.23), совместимый с клиентом **v0.7.29+59**. Go 1.27.3, PostgreSQL 17.11; миграция схемы **40→41**, голосовой профиль **3** сохранён. Добавлены личные подписки, персональная лента и экспорт собственного содержимого в JSON. Подписки не расширяют доступ; экспорт исключает чужой текст, MLS и секреты. Сервер повторно проверяет действующую сессию и права чтения.

[Контракт подписок ADR-059](https://github.com/space-protocol-project/space-protocol/blob/main/docs/adr/059-participant-follows.md), [экспорта ADR-060](https://github.com/space-protocol-project/space-protocol/blob/main/docs/adr/060-user-content-export.md), [изменения](Changelog.md), [отчёт совместимого выпуска](https://github.com/space-protocol-project/space-app/blob/main/docs/MARKDOWN-EXPORT-RELEASE.md). Общий поиск чатов, форума и ленты, ответы на комментарии, репосты и цитаты доступны начиная с v0.7.22.

[Релизный CI](https://github.com/space-protocol-project/space-server/actions/runs/40406370390) успешен на Windows x64, Linux x64 и macOS arm64: TLS, обновление, перенос, восстановление MLS, полные Go/Flutter и проверка гонок. [Публичные архивы проверены](https://github.com/space-protocol-project/space-app/actions/runs/40409781309): полное скачивание, размеры, SHA-256 и CRC; во всех архивах схема 41. [Опубликованный Linux-установщик](https://github.com/space-protocol-project/space-server/actions/runs/40409785065) также прошёл проверку. Сборки и тесты выполняются только в GitHub CI; физический Android и эмулятор не используются.

Перед обновлением сохраните резервную копию пространства и ключей MLS. Установленные данные не заменяются комплектом runtime.

Runtime **v0.7.15** включает `FeedService`, capability `core.feed.v1` и миграцию 31: отдельные права публикации и комментирования, файлы, жалобы и атомарную модерацию. Для ленты требуется клиент **v0.7.19**, закрепляющий этот runtime. [Результаты совместимого выпуска](../space-app/docs/FEED-RELEASE.md), [возможности и результаты проверки](../space-app/docs/FEED.md), [контракт ADR-046](../space-protocol/docs/adr/046-feed.md).

Runtime v0.7.16 включает `CommunitySearchService`, capability `core.community.search.v1` и миграцию 31→32. Поиск тем, ответов, публикаций и комментариев ограничен выбранным разделом и повторно проверяет серверные права на каждой странице. [Контракт ADR-047](../space-protocol/docs/adr/047-community-search.md), [поведение клиента](../space-app/docs/COMMUNITY-SEARCH.md).

В runtime v0.7.17 добавлены `core.feed.read.v1` и миграция 32→33: персональные монотонные отметки публикаций и комментариев ленты, общие для устройств principal. Списки и сводка учитывают только видимое непрочитанное; каждый запрос проверяет доступ. [Контракт ADR-049](../space-protocol/docs/adr/049-feed-read.md), [поведение и проверки клиента](../space-app/docs/FEED-READ.md).


В v0.7.16 также входит персональная сводка `CommunityOverviewService` с capability `core.community.overview.v1`: свежие темы и публикации, непрочитанные ответы в собственных темах и число жалоб для модератора. Каждый запрос проверяет доступ к выбранному разделу. Новая миграция сверх схемы 32 не требуется. [Контракт ADR-048](../space-protocol/docs/adr/048-community-overview.md), [поведение сводки](../space-app/docs/DASHBOARD.md).

Предыдущий [runtime v0.7.10](https://github.com/space-protocol-project/space-server/releases/tag/v0.7.10): форум с темами, ответами, серверной модерацией и вложениями до пяти файлов по 20 МиБ. Схема PostgreSQL 27; требуется клиент v0.7.13. Перед миграцией сохраните резервную копию. Форум не использует MLS/E2EE.

В runtime v0.7.10 голосовые комнаты поддерживают настраиваемый лимит от 2 до 32 участников (по умолчанию 8) на компьютерах и Android: схема 27, голосовой профиль 3, WebRTC/Opus, STUN/TURN и временный доступ к ретранслятору. Для изменения лимита и форума обновите клиент до v0.7.13. Для Android используйте клиент v0.7.10; рамки речи доступны начиная с v0.7.9: сервер передаёт стабильное audio_slot, а клиент связывает его с аудиодорожкой. Старые клиенты подключаются только при лимите 8. Уменьшение лимита не отключает активных участников. Вложения выключены по умолчанию только в новых пространствах; миграция сохраняет прежние значения. [Настройка своего TURN для интернета и CGNAT](deploy/turn/README.md).

Запуск с нуля одной командой (приложение v0.8.0 и runtime v0.8.0):
[инструкция для Linux-сервера и первого входа с телефона](https://github.com/space-protocol-project/space-app/blob/main/docs/STANDALONE-SERVER.md).

```sh
curl -fsSL https://raw.githubusercontent.com/space-protocol-project/space-server/v0.8.0/packaging/install.sh | SPACE_VERSION=v0.8.0 sh
```

Первый вошедший по одноразовой ссылке из терминала становится владельцем.

Для постоянной работы на VPS с systemd используйте новый установщик:

```sh
curl -fsSL https://raw.githubusercontent.com/space-protocol-project/space-server/main/packaging/install.sh | sh -s -- --service
```

Он запускает сервер от текущего пользователя, включает запуск при загрузке и сохраняет данные после выхода из SSH. Если не хватает прав на linger, установщик покажет команду `sudo loginctl enable-linger "$USER"`; выполните её один раз и повторите установку. Root для самого сервера не используется. Этот режим совместим с runtime v0.3.3 и новее.

Управление: `systemctl --user status space-server`, `systemctl --user restart space-server`, `systemctl --user stop space-server`. Журнал: `journalctl --user -u space-server -n 50`. Подробности, отключение автозапуска и путь к одноразовой ссылке — в [инструкции](https://github.com/space-protocol-project/space-app/blob/main/docs/STANDALONE-SERVER.md#постоянная-работа-на-vps-systemd).

Владелец может назначать несколько администраторов. Нужны обычный пользователь
Linux x64, curl/unzip/sha256sum и открытый TCP 8443. Требуются приложение и runtime v0.3.3 или новее.

Домашний runtime v0.2.4 поддерживает перенос пространства через приложение:
[инструкция экспорта и импорта](https://github.com/space-protocol-project/space-app/blob/main/docs/SPACE-TRANSFER.md).
Для остановленной домашней установки CLI предоставляет `-home-export`,
`-home-import`, `-home-import-to` и необязательный `-public-ip` нового компьютера.
Импорт создаёт новый каталог и не заменяет существующие данные. Ключ владельца
восстанавливается отдельно от архива сервера.

В исходниках следующего выпуска добавлена проверка готового архива:

```text
space-server -home-verify-export /path/to/space.zip
```

Команда не запускает сервер или PostgreSQL, не распаковывает файлы и не меняет
действующее пространство. Она проверяет состав ZIP, ограничения размеров,
версию схемы, CRC и SHA-256 файлов и возвращает JSON с server ID, схемой,
количеством файлов и суммарным размером после распаковки. Ключи, конфигурация
и тексты сообщений в отчёт не включаются. Возможность объявляется в
`-runtime-info` как `portableInspectionVersion: 1`.

Успешная проверка означает целостность и совместимость формата с этим runtime,
но не подлинность источника и не доказанное восстановление базы. Для приёмки
резервной копии по-прежнему нужен импорт в отдельное окружение и проверка
сообщений и файлов. Это проверка переносимого ZIP; физические копии
`-home-backup` имеют другой формат. Команда доступна с v0.4.3; v0.4.2 её не содержит.

Сервер Space и домашний runtime. Один и тот же Go-сервер предоставляет discovery, gRPC/HTTP API, PostgreSQL, чат, каналы и ACL, участников, приглашения, устройства, recovery и сопряжение.

- [Общее Flutter-приложение](https://github.com/space-protocol-project/space-app).
- [Протокол и архитектура](https://github.com/space-protocol-project/space-protocol).
- [Домашний запуск из приложения](docs/HOME-SERVER.md).
- [Выпуски сервера и runtime](https://github.com/space-protocol-project/space-server/releases).

## Сервер и домашний runtime

Сервер — сама программа. Домашний runtime — комплект этой программы с PostgreSQL для автоматической установки и запуска кнопкой «Выйти в эфир» в Space. Отдельной серверной реализации или браузерной админки нет. Повторный запуск сохраняет прежнюю базу, server ID, сертификат, владельца и участников; «Отключиться» останавливает процессы с сохранением данных.

## Запуск для разработки

В готовящемся выпуске доступна диагностика без запуска базы: `space-server doctor -home-data 'C:\Space\data'`. Для проверки из другой сети укажите `-endpoint`, `-fingerprint` и `-server-id` из команды, скопированной в «Эфире». Подробности и ограничения: [диагностика сети](https://github.com/space-protocol-project/space-app/blob/main/docs/NETWORK-DOCTOR.md). Локальный успех не подтверждает внешнюю доступность; doctor доступен в runtime v0.3.3 и новее.

Нужны Go 1.27.2+ и отдельная PostgreSQL. Из корня этого репозитория:

```powershell
$env:SPACE_DATABASE_URL = 'postgres://space:YOUR_PASSWORD@127.0.0.1:5432/space?sslmode=disable'
go run ./cmd/space-server
```

Обычный режим использует loopback HTTP 127.0.0.1:8080 и gRPC 127.0.0.1:9090. Изменение портов: `-http` и `-grpc`. Временный режим `-demo` не предназначен для настоящего домашнего пространства.

Домашний режим использует `-home-data`, `-home-runtime`, `-public-ip`, `-https` и `-managed`; приложение задаёт их автоматически. По умолчанию HTTPS и HTTP/2 gRPC используют TCP 8443. PostgreSQL и внутренние входы остаются на loopback. Сертификат самоподписанный, его отпечаток закрепляется в приложении и передаётся в ссылке приглашения. Публичный вход закрыт до локального назначения владельца.

Миграции встроены в бинарник и проверяются по checksum. Разделение репозиториев не изменяет SQL-миграции, формат базы или identity сервера. Не удаляйте кластер для обновления бинарника.

## Проверки и релизы

Предыдущий выпуск — [runtime v0.7.10](https://github.com/space-protocol-project/space-server/releases/tag/v0.7.10), схема PostgreSQL 27 и голосовой профиль 3: темы, ответы, вложения и серверная модерация форума; голосовые комнаты на 2–32 участника. [Релизный CI](https://github.com/space-protocol-project/space-server/actions/runs/39833473874) полностью успешен на Windows/macOS/Linux, включая TLS-вход, обновление с сохранением данных, перенос, старые архивы и восстановление MLS с тремя устройствами и зашифрованным файлом. Go-тесты с PostgreSQL и проверкой гонок, анализ безопасности, совместимость клиента и системное хранилище Windows прошли. Три runtime-архива, три JSON-дескриптора и SHA256SUMS.txt полностью скачаны: размеры и SHA-256 совпадают с GitHub API и контрольными суммами. Выпуск Latest, не draft и не prerelease.

В [клиенте v0.7.19](https://github.com/space-protocol-project/space-app/releases/tag/v0.7.19) каталог закреплён на runtime v0.7.15. Сначала обновите клиент, сохраните резервную копию пространства и ключей MLS, затем примените обновление runtime в «Эфире». Прежние комнаты сохраняют лимит 8. Изменённый лимит требует обновления приложений всех участников. Акустическая приёмка 32 людей, длительная нагрузка на реальных устройствах и публичный TURN остаются отдельными проверками.

Новые каталоги копий и восстановления защищены до записи данных: Windows — владелец процесса и SYSTEM, Unix — 0700. Права уже созданных копий автоматически не изменяются. Контрольные суммы архивов доступны в [SHA256SUMS.txt](https://github.com/space-protocol-project/space-server/releases/latest/download/SHA256SUMS.txt).

`go test ./...` и `go vet ./...` выполняются из корня. Полные PostgreSQL и сквозные Flutter/Go/WebCrypto-проверки выполняются в CI. Flutter берётся из `space-app`; локальный Docker для этой работы не запускается. [Обновление Go и зависимостей, локальные проверки сервера и runtime](docs/DEPENDENCY_UPDATES.md).

Теги `vX.Y.Z` собирают Windows x64, macOS arm64 и Linux x64, проверяют перезапуск и приглашённого участника через TLS, затем публикуют архивы и SHA-256. Описание берётся из соответствующего раздела [Changelog.md](Changelog.md). Тег и описание проверяются до сборки. Без полного успешного CI публикации нет. Файлы сначала загружаются в черновик; повторный запуск продолжает его, а уже опубликованный выпуск не перезаписывает. Обычный тег создаёт релиз, суффикс версии — prerelease. Обычные и ручные сборки сохраняют артефакты.

Контракт `.proto` находится в `space-protocol`. Go bindings коммитятся в `gen/`. Из этого репозитория генерация выполняется `buf generate ../space-protocol/protocol/proto --template buf.gen.yaml` с установленными генераторами, версии — в руководстве генерации протокола.

Проект экспериментальный. Интернет-доступ через домашний роутер, установка и защищённое хранилище macOS/Linux, аварийное завершение, резервные копии и смена публичного IP требуют отдельных проверок или следующих этапов.

Сервер сохраняет время Content.created_at_unix_ms. Миграция 010 сохраняет неизвестное время старой истории; создание, повтор, история и события возвращают одно сохранённое значение. [CI 38167063307](https://github.com/space-protocol-project/space-server/actions/runs/38167063307) подтвердил PostgreSQL и совместимость Dart/Go для 55a6a53. Ранее опубликованные выпуски не заменяются.

## ConnectRPC

Все 39 RPC доступны на маршрутах `/space.v1.…/…`; discovery объявляет `connect_endpoint: "/"`. Connect включён постоянно. Основной Flutter-клиент, управление, recovery и pairing используют Connect с Protobuf; диагностические сценарии также проверяют JSON.

HTTP-авторизация выполняется до декодирования тела. Запросы передаются внутреннему gRPC-сервису с общей проверкой токенов, подписей и прав; это внутренняя реализация, не запасной транспорт Flutter-клиента. Публичный HTTPS больше не направляет внешние gRPC-запросы в этот сервис. Размер тела и распакованного запроса ограничен 16 КиБ; отмена Connect-потока отменяет upstream, запись имеет возобновляемый таймаут 20 секунд.

Генерация: `buf generate ../space-protocol/protocol/proto --template buf.connect.gen.yaml`. Connect-Go/генератор закреплены на 1.21.1. Flutter проверяет точный TLS-отпечаток и срок сертификата до отправки запроса. Для нового клиента нужен Connect-runtime; прежние серверы не поддерживаются и не вызывают автоматический fallback.

## Подготовка выпуска

1. Перенесите готовые пункты из `Unreleased` в раздел `## vX.Y.Z` в [Changelog.md](Changelog.md).
2. Закоммитьте изменения и отправьте main.
3. Создайте и отправьте тег в этом репозитории, например:

```powershell
git tag -a v0.3.0 -m "Space Server v0.3.0"
git push origin v0.3.0
```

Workflow `home-runtime.yml` проверит сервер, соберёт архивы и дескрипторы для трёх платформ и опубликует GitHub Release с описанием и `SHA256SUMS.txt`. Версии приложения и сервера независимы. После серверного выпуска закрепите стабильный runtime в space-app через `packaging/pin-home-runtime.py`, затем выпускайте приложение. Предварительные серверные теги пока не поддержаны механизмом закрепления runtime основного приложения.

Форум в runtime v0.7.11 / клиенте v0.7.14: схема 28, `core.forum.v1`, `core.forum.attachments.v1` и `core.forum.navigation.v1`, отдельный ForumService через gRPC и Connect. Темы и ответы используют ACL раздела; закрытие требует read/manage и запрещает новые ответы на сервере. [Контракт и решение](https://github.com/space-protocol-project/space-protocol/blob/codex/voice-rooms/docs/adr/045-forum.md).
