# Аудит сложности приложения и сервера — 14 октября 2026

В собственном рабочем коде, измеренном анализатором, не осталось функций с COG > 15. Максимум — 15 в обоих репозиториях. Это результат проверки ветвлений, а не доказательство минимально возможной сложности или понятности любого алгоритма начинающему. Криптография, SQL и системные API сохраняют необходимые проверки.

Использован [treesitter_cyclomatic_complexity](https://github.com/Hecatoncheir/treesitter_cyclomatic_complexity), коммит `7b216f2344d87a910e6c1ffbd974fe60950c15ce`, Neovim 0.12.5, режим `inline`: вложенные замыкания включаются в оценку окружающей функции. Измеряются Dart, Go, Python и JavaScript из Git, включая новые неигнорируемые файлы. Нативный C/C++, Swift, PowerShell, shell и Inno Setup не имеют численной оценки этим инструментом. У Dart COG нет независимой эталонной реализации.

## Сводка

| Репозиторий / категория | Измеренных файлов | Функций | COG > 15 | Максимум COG |
|---|---:|---:|---:|---:|
| app / production | 90 | 1550 | 0 | 15 |
| app / generated | 9 | 2806 | 0 | 9 |
| app / test | 74 | 217 | 8 | 76 |
| app / tool | 21 | 64 | 6 | 51 |
| app / vendor | 5 | 92 | 3 | 31 |
| app / prototype | 4 | 102 | 4 | 84 |
| server / production | 39 | 321 | 0 | 15 |
| server / test | 31 | 89 | 21 | 50 |
| server / generated | 4 | 967 | 6 | 52 |
| server / tool | 14 | 45 | 5 | 45 |
| server / vendor | 2 | 19 | 8 | 853 |

Тесты, инструменты, прототипы, сгенерированные файлы и сторонние библиотеки учитываются отдельно. Завершён оставшийся список рабочего кода: 42 функции приложения и 21 сервера. Превышения в тестах и инструментах перечислены ниже; их упрощение не заявляется выполненным.

## Все функции из оставшегося списка

| Репозиторий | Файл | Функция | COG до | COG после |
|---|---|---|---:|---:|
| app | `client/lib/src/camera_qr.dart` | `cameraCardInWorker` | 16 | 13 |
| app | `client/lib/src/chat_controller.dart` | `unreadCount` | 16 | 10 |
| app | `client/lib/src/chat_controller.dart` | `_loadPositions` | 21 | 7 |
| app | `client/lib/src/chat_controller.dart` | `_connect` | 24 | 14 |
| app | `client/lib/src/chat_controller.dart` | `_selectChannel` | 17 | 14 |
| app | `client/lib/src/chat_controller.dart` | `_watchCatalog` | 16 | 14 |
| app | `client/lib/src/chat_history.dart` | `write` | 22 | 4 |
| app | `client/lib/src/core.dart` | `parseConnectionInput` | 16 | 9 |
| app | `client/lib/src/core.dart` | `discover` | 18 | 15 |
| app | `client/lib/src/core.dart` | `adminRequest` | 28 | 13 |
| app | `client/lib/src/core.dart` | `connect` | 20 | 13 |
| app | `client/lib/src/core.dart` | `_authorize` | 31 | 15 |
| app | `client/lib/src/core.dart` | `recordFromRecovery` | 21 | 15 |
| app | `client/lib/src/home_backup.dart` | `verifyHomeBackup` | 26 | 13 |
| app | `client/lib/src/home_controller.dart` | `_load` | 17 | 7 |
| app | `client/lib/src/home_controller.dart` | `_start` | 25 | 8 |
| app | `client/lib/src/home_runtime.dart` | `extractVerifiedHomeArchive` | 18 | 10 |
| app | `client/lib/src/home_update_executor.dart` | `recover` | 24 | 14 |
| app | `client/lib/src/pairing.dart` | `verifiedRecord` | 20 | 7 |
| app | `client/lib/src/preferences.dart` | `_load` | 21 | 8 |
| app | `client/lib/src/preferences.dart` | `get theme` | 21 | 4 |
| app | `client/lib/src/recovery_qr.dart` | `cardFromArtifact` | 19 | 12 |
| app | `client/lib/src/root_history.dart` | `verifyRootHistory` | 25 | 14 |
| app | `client/lib/src/root_rotation.dart` | `prepareRootRotation` | 22 | 14 |
| app | `client/lib/src/root_rotation.dart` | `finishRootRotation` | 16 | 14 |
| app | `client/lib/ui/admin_panel.dart` | `serverAdministration` | 25 | 9 |
| app | `client/lib/ui/app_updates_view.dart` | `build` | 22 | 12 |
| app | `client/lib/ui/camera_scanner.dart` | `start` | 18 | 15 |
| app | `client/lib/ui/chat_view.dart` | `send` | 18 | 11 |
| app | `client/lib/ui/pairing_panel.dart` | `poll` | 17 | 13 |
| app | `client/lib/ui/recovery_file_picker.dart` | `build` | 17 | 8 |
| app | `client/lib/ui/recovery_panel.dart` | `run` | 18 | 4 |
| app | `client/lib/ui/recovery_panel.dart` | `export` | 21 | 9 |
| app | `client/lib/ui/recovery_panel.dart` | `restore` | 19 | 13 |
| app | `client/lib/ui/recovery_panel.dart` | `revoke` | 18 | 14 |
| app | `client/lib/ui/server_inbox.dart` | `build` | 16 | 11 |
| app | `client/lib/ui/shell.dart` | `header` | 33 | 2 |
| app | `packages/space_admin_ui/lib/space_admin_ui.dart` | `build` | 20 | 13 |
| app | `packages/space_admin_ui/lib/src/control_spacing.dart` | `spaceControls` | 16 | 15 |
| app | `packages/space_admin_ui/lib/src/recovery_tools_view.dart` | `build` | 30 | 2 |
| app | `packages/space_admin_ui/lib/src/server_icon.dart` | `safeServerSvg` | 19 | 14 |
| app | `packages/space_admin_ui/lib/src/server_icon.dart` | `validatedServerIcon` | 24 | 14 |
| server | `cmd/space-demo/main.go` | `run` | 18 | 15 |
| server | `cmd/space-server/main.go` | `run` | 20 | 14 |
| server | `internal/home/certificate.go` | `Certificate` | 17 | 9 |
| server | `internal/home/runtime.go` | `StartDatabase` | 22 | 10 |
| server | `internal/postgres/admin.go` | `UpdateSettings` | 19 | 13 |
| server | `internal/postgres/auth.go` | `CompleteChallenge` | 22 | 8 |
| server | `internal/postgres/auth.go` | `register` | 27 | 15 |
| server | `internal/postgres/auth.go` | `login` | 28 | 13 |
| server | `internal/postgres/channels.go` | `channelAccess` | 26 | 12 |
| server | `internal/postgres/channels.go` | `channelList` | 25 | 9 |
| server | `internal/postgres/channels.go` | `UpdateChannel` | 21 | 15 |
| server | `internal/postgres/channels.go` | `DeleteChannel` | 17 | 15 |
| server | `internal/postgres/channels.go` | `UpdateChannelAccess` | 28 | 13 |
| server | `internal/postgres/compatibility.go` | `CheckCompatibility` | 19 | 7 |
| server | `internal/postgres/pairing.go` | `PollPairing` | 18 | 11 |
| server | `internal/postgres/recovery.go` | `createRecoveryChallenge` | 30 | 13 |
| server | `internal/postgres/recovery.go` | `completeRecovery` | 20 | 15 |
| server | `internal/postgres/rotation.go` | `CreateRootRotation` | 28 | 15 |
| server | `internal/postgres/rotation.go` | `CompleteRootRotation` | 37 | 9 |
| server | `internal/postgres/server_icon.go` | `validateServerIcon` | 17 | 12 |
| server | `internal/postgres/subscribe.go` | `Subscribe` | 24 | 13 |

## Что разделено по смыслу

- Клиент: подготовка записи устройства, сборка запроса авторизации, проверка результатов и маршруты административных операций.
- Домашний runtime: конфигурация, наблюдение процесса, ожидание готовности, проверка архивов и восстановление из копии.
- Идентичность: описание запроса ротации, подписи, связь с прежней записью и подготовка новой записи.
- Интерфейс: заголовок, действия обновления, восстановление, сопряжение, карточка доступа и список устройств.
- Сервер: подготовка базы и сертификата, внутреннее RPC-соединение и завершение процесса.
- PostgreSQL: чтение и проверка challenge, регистрационная политика, membership, блокировки ротации и применение изменений в одной транзакции.
- Каналы: выбор блокировки, проверка видимости и управления, проверка участников ACL, запись правил и чтение списка каналов.
- Подписки: отправка пачки событий, heartbeat и ожидание следующей пачки.

SQL-запросы, параметры, порядок блокировок и операций сохранены. Проверки сертификатов, подписей, размеров, контрольных сумм, прав доступа и запрета перезаписи данных не ослаблялись ради метрики.

## Превышения в тестах и инструментах

| Репозиторий | Категория | Файл:строка | Функция | CC | COG |
|---|---|---|---|---:|---:|
| app | test | `client/test/home_update_executor_test.dart:27` | `main` | 36 | 76 |
| app | tool | `client/tool/home_server_drill.dart:95` | `main` | 25 | 51 |
| app | test | `client/test/admin_invites_localization_test.dart:8` | `main` | 12 | 37 |
| app | tool | `client/tool/native_vault_drill.dart:11` | `main` | 26 | 33 |
| app | test | `client/test/dashboard_workspaces_test.dart:45` | `main` | 10 | 27 |
| app | tool | `client/tool/app_update_install_probe.dart:11` | `main` | 24 | 26 |
| app | test | `client/test/app_update_install_test.dart:10` | `main` | 10 | 24 |
| app | tool | `packaging/publish-release.py:16` | `publish` | 15 | 24 |
| app | test | `client/test/rpc_connection_test.dart:45` | `main` | 9 | 23 |
| app | test | `client/test/admin_people_invites_test.dart:23` | `main` | 8 | 22 |
| app | tool | `client/tool/interoperability.dart:20` | `main` | 21 | 20 |
| app | test | `client/test/chat_design_test.dart:44` | `main` | 8 | 19 |
| app | test | `client/test/admin_sidebar_test.dart:65` | `main` | 9 | 18 |
| app | tool | `client/tool/first_owner_interoperability.dart:19` | `main` | 19 | 16 |
| server | test | `internal/postgres/auth_test.go:52` | `TestAuthGatewayReplayEventsAndRevoke` | 46 | 50 |
| server | test | `internal/postgres/membership_test.go:21` | `TestInvitationsAndLiveMembership` | 46 | 48 |
| server | test | `internal/postgres/pairing_test.go:19` | `TestPairingApprovalClaimCancellationAndExpiry` | 49 | 48 |
| server | tool | `tools/crypto_reference/identity.mjs:48` | `proof` | 62 | 45 |
| server | test | `internal/home/certificate_test.go:15` | `TestInvalidSavedCertificateIsNotReplaced` | 17 | 44 |
| server | test | `internal/postgres/first_owner_test.go:20` | `TestFirstSuccessfulLoginClaimsExactlyOneOwner` | 36 | 44 |
| server | test | `internal/postgres/channels_test.go:174` | `TestChannelsIsolationPermissionsAndPersistence` | 42 | 41 |
| server | test | `internal/postgres/rotation_test.go:24` | `TestRootRotationStablePrincipalAndRevocation` | 45 | 41 |
| server | test | `internal/postgres/store_test.go:64` | `TestConcurrentInitializationAndRestart` | 31 | 37 |
| server | test | `internal/postgres/stream_test.go:25` | `TestSubscribeReplayLiveHTTPAndRevoke` | 39 | 37 |
| server | tool | `packaging/build-home-runtime.py:31` | `build` | 22 | 34 |
| server | test | `internal/home/backup_permissions_windows_test.go:12` | `TestBackupDirectoryProtectsRootAndChildren` | 22 | 33 |
| server | test | `internal/postgres/channels_test.go:388` | `TestChannelHTTPAndConcurrentRevision` | 26 | 33 |
| server | test | `internal/home/runtime_test.go:39` | `TestBundledPostgresPreservesIdentityAndData` | 12 | 28 |
| server | test | `internal/postgres/admin_test.go:33` | `TestOwnerBootstrapScopesAndSettings` | 27 | 28 |
| server | tool | `tools/crypto_reference/root-history.mjs:11` | `verifyRootHistory` | 39 | 26 |
| server | test | `internal/home/backup_test.go:82` | `TestStoppedDatabaseBackupAndRestore` | 24 | 24 |
| server | tool | `packaging/publish-release.py:16` | `publish` | 15 | 24 |
| server | test | `internal/postgres/recovery_test.go:12` | `TestRecoveryAuthorityAndRevocation` | 19 | 20 |
| server | test | `internal/postgres/server_icon_test.go:28` | `TestServerIconSharedAndPreserved` | 19 | 20 |
| server | test | `internal/postgres/unread_test.go:8` | `TestMessageSequencesAndPrivateWatermarks` | 23 | 20 |
| server | test | `internal/transport/connect_test.go:88` | `TestConnectKeepsInternalAuthAndCancelsStream` | 18 | 20 |
| server | test | `internal/transport/http_test.go:52` | `TestGatewayAndNativeShareState` | 18 | 18 |
| server | tool | `tools/crypto_reference/recovery-qr.mjs:4` | `qrRaster` | 8 | 17 |
| server | test | `internal/postgres/channel_watch_test.go:24` | `TestWatchChannelsWithoutReadableChannelAndRevocation` | 20 | 16 |

## Повторение проверки

```powershell
python -X utf8 tools/complexity_audit.py --analyzer C:/path/to/treesitter_cyclomatic_complexity --server ../space-server --output complexity.json
```

Нужны Neovim и парсеры Go/Dart/Python/JavaScript в `.ci/parsers/parser` анализатора. Закреплённые версии описаны в его `scripts/install-parsers.sh`. Скрипт выдаёт JSON с перечнем файлов, категориями, строками, CC и COG и возвращает ошибку при неуспешном запуске анализатора.

Изменения после коммитов 409cdd4 (приложение) и 534dd32 (сервер) опубликованы в выпусках, указанных ниже. Старое пользовательское изменение space-bubble-design.png не входит в этот рефакторинг.

## Проверка итоговых исходников

- flutter analyze --no-pub: без замечаний.
- flutter test --no-pub: 238 успешных, 4 штатных пропуска (установка/обновление runtime и промоматериалы).
- flutter build windows --release --no-pub: успешно.
- Windows debug: запуск и hot reload успешны, ошибок Dart не обнаружено.
- go test ./... и go vet ./...: успешно.
- go test ./... -count=1: успешно с новой временной PostgreSQL 17 и SPACE_HOME_RUNTIME; пакет postgres — 37.735 с, home — 30.455 с. Проверены авторизация, сопряжение, восстановление, ротация, membership/ACL и резервное копирование. Пользовательская база не использовалась; временный процесс PostgreSQL остановлен.
- Живой demo сервер: healthz 204, discovery содержит connect_endpoint /, завершение после закрытия управляющего stdin с кодом 0.
- git diff --check: ошибок нет.

Сборки Linux/macOS, ручной сценарий Windows UI Automation и новый прогон CI на этом этапе не выполнялись. Проверка запуска и hot reload не является полной ручной проверкой всех экранов. SQL и порядок блокировок сопоставлены с исходной последовательностью; интеграционные тесты подтверждают покрытые ими сценарии.

## Опубликованная совместимая пара

14 октября опубликованы Space v0.2.4 и домашний runtime v0.2.3. Оба выпуска содержат полный комплект для Windows x64, Linux x64 и macOS arm64; GitHub Latest указывает на эти версии. Дескрипторы runtime в приложении проверены по SHA-256 и размерам опубликованных архивов.

Release CI приложения: https://github.com/space-protocol-project/space-app/actions/runs/38543970928 — success.
Release CI runtime: https://github.com/space-protocol-project/space-server/actions/runs/38542385203 — success после повтора Windows-задания (первый запуск: временный TLS-сбой скачивания PostgreSQL до сборки).

Коммит рабочего кода приложения: 5c73245; тег v0.2.4 закреплён на 83ed19b с опубликованным манифестом runtime. Коммит и тег сервера v0.2.3: a6d276b. Старое пользовательское изменение space-bubble-design.png осталось вне коммитов.


## Перенос пространства и Android

Повторная проверка включает экспорт/импорт, подпись нового адреса, постоянную
идентичность, выбор QR и блокировку домашнего сервера на мобильных платформах.
В собственном измеренном рабочем коде по-прежнему нет COG выше 15. Kotlin, Gradle
и XML численно не измеряются; конфигурация Android проверяется сборкой APK.
Сквозной перенос с новым IP, старой карточкой владельца после ротации root и
HTTPS Connect выполнен на отдельной PostgreSQL. Все 248 обычных Flutter-тестов
прошли, четыре проверки штатно пропущены.
