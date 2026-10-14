# Аудит сложности приложения и сервера — 14 октября 2026

Проверка обнаружила сложные функции: весь проект пока нельзя считать кодом с минимальной когнитивной сложностью. Ниже сохранены все превышения COG > 15 в собственном рабочем коде, а также отдельный список тестов и инструментов. Значение 15 — ориентир для проверки, а не доказательство качества или обязательный предел алгоритма.

Использован [treesitter_cyclomatic_complexity](https://github.com/Hecatoncheir/treesitter_cyclomatic_complexity) на коммите `7b216f2344d87a910e6c1ffbd974fe60950c15ce`, Neovim 0.12.5, режим `inline`: вложенные замыкания включаются в оценку окружающей функции. Измерены все файлы Dart, Go, Python и JavaScript, которые перечисляет Git, включая новые неигнорируемые файлы. Нативный C/C++, Swift, PowerShell, shell и Inno Setup этим инструментом не поддерживаются и не получили численной оценки. Прохождение тестов не заменяет оценку их сложности. У Dart COG нет независимой эталонной реализации.

Сгенерированные файлы, сторонние библиотеки и прототипы учтены отдельно; их показатели не используются для утверждений о сложности рабочего кода приложения.

## Выполненные упрощения

| Функция | COG до | COG после |
|---|---:|---:|
| ChatController._watch | 71 | 13 |
| ChatView._build (ранее build) | 87 | 10 |
| space-server.run | 120 | 33 |
| safeSVG | 42 | 7 |

Цикл подписки отделён от проверки событий и восстановления доступа. Экран чата разделён на список, строку сообщения, выбор канала, пустое состояние, восстановление прокрутки и обработку клавиатуры. Запуск сервера отделён от служебных команд, подготовки владельца и записи состояния. Проверка атрибутов SVG выделена отдельно. Это оценки конкретных функций; перенос логики в помощники не означает удаления всех ветвлений проекта.

После переноса на события Bloc цикл каталога `_watchCatalog` снизился с COG 39 до 16. Его обработчики `_catalogChanged`, `_recoverCatalog`, `_renewCatalogLogin` имеют COG 4, 6 и 3. Обработчик кадра `_receiveFrame` имеет COG 2. Новая инфраструктура событий и фабрики состояний не добавили превышений COG 15.

## Повторение проверки

В репозитории приложения:

```powershell
python -X utf8 tools/complexity_audit.py --analyzer C:/path/to/treesitter_cyclomatic_complexity --server ../space-server --output complexity.json
```

Нужны Neovim и парсеры Go/Dart/Python/JavaScript в `.ci/parsers/parser` анализатора; закреплённые версии и сборка описаны в его `scripts/install-parsers.sh`. Скрипт не загружает зависимости, возвращает ошибку при неуспешном запуске анализатора и выдаёт JSON с перечнем файлов, категориями, строками, CC и COG. Полученный JSON позволяет повторно найти функцию даже после изменения номеров строк. Пустые/декларативные файлы могут не содержать измеряемых функций. Для категорий без численной оценки требуется отдельная проверка подходящим инструментом.

## Сводка

| Репозиторий / категория | Измеренных файлов | Функций | COG > 15 | Максимум COG |
|---|---:|---:|---:|---:|
| app / production | 85 | 1454 | 45 | 33 |
| app / tool | 19 | 54 | 6 | 51 |
| app / test | 69 | 210 | 8 | 76 |
| app / prototype | 4 | 102 | 4 | 84 |
| app / generated | 9 | 2762 | 0 | 9 |
| app / vendor | 5 | 92 | 3 | 31 |
| server / production | 33 | 249 | 24 | 37 |
| server / tool | 14 | 45 | 5 | 45 |
| server / test | 30 | 86 | 20 | 50 |
| server / prototype | 0 | 0 | 0 | 0 |
| server / generated | 4 | 967 | 6 | 52 |
| server / vendor | 2 | 19 | 8 | 853 |

## Приоритет дальнейших изменений

1. Запуск сервера: выделить конфигурацию домашнего HTTPS, регистрацию сервисов и жизненный цикл слушателей, сохраняя порядок закрытия ресурсов и ошибок.
2. Авторизация и ротация ключей: разделить проверку входных данных, формирование transcript и транзакционные операции; изменения проверять интеграционными тестами с PostgreSQL, повторным использованием challenge и отзывом доступа.
3. Каталог каналов, установка/обновление/восстановление runtime: разделить состояния и операции, сохраняя отмену, идентичность сессии и восстановление после сбоя.
4. Большие экраны shell/home/recovery: выделять законченные части интерфейса с явными параметрами; сохранять фокус, черновики, ключи виджетов и локализацию.
5. Служебные скрипты и тесты: разбивать сценарии по самостоятельным проверкам, не скрывая условия в непрозрачных универсальных помощниках.

## Все превышения в рабочем коде

| Репозиторий | Файл:строка | Функция | CC | COG |
|---|---|---|---:|---:|
| app | `client/lib/ui/shell.dart:1181` | `header` | 21 | 33 |
| app | `client/lib/ui/chat_view.dart:116` | `jumpToMessage` | 21 | 32 |
| app | `client/lib/src/core.dart:913` | `_authorize` | 32 | 31 |
| app | `client/lib/src/app_updates.dart:283` | `_download` | 25 | 30 |
| app | `packages/space_admin_ui/lib/src/recovery_tools_view.dart:77` | `build` | 33 | 30 |
| app | `client/lib/src/core.dart:599` | `adminRequest` | 23 | 28 |
| app | `client/lib/src/home_backup.dart:40` | `verifyHomeBackup` | 26 | 26 |
| app | `client/lib/src/home_controller.dart:399` | `_start` | 21 | 25 |
| app | `client/lib/src/root_history.dart:38` | `verifyRootHistory` | 37 | 25 |
| app | `client/lib/ui/admin_panel.dart:52` | `serverAdministration` | 10 | 25 |
| app | `client/lib/src/app_updates.dart:49` | `compareTo` | 14 | 24 |
| app | `client/lib/src/chat_controller.dart:619` | `_connect` | 22 | 24 |
| app | `client/lib/src/home_update_executor.dart:263` | `recover` | 18 | 24 |
| app | `packages/space_admin_ui/lib/src/server_icon.dart:97` | `validatedServerIcon` | 27 | 24 |
| app | `client/lib/src/chat_history.dart:197` | `write` | 16 | 22 |
| app | `client/lib/src/root_rotation.dart:19` | `prepareRootRotation` | 33 | 22 |
| app | `client/lib/ui/app_updates_view.dart:59` | `build` | 24 | 22 |
| app | `client/lib/src/chat_controller.dart:381` | `_loadPositions` | 14 | 21 |
| app | `client/lib/src/core.dart:1443` | `recordFromRecovery` | 25 | 21 |
| app | `client/lib/src/preferences.dart:84` | `_load` | 15 | 21 |
| app | `client/lib/src/preferences.dart:189` | `get theme` | 21 | 21 |
| app | `client/lib/ui/recovery_panel.dart:178` | `export` | 17 | 21 |
| app | `client/lib/src/core.dart:765` | `connect` | 20 | 20 |
| app | `client/lib/src/pairing.dart:103` | `verifiedRecord` | 32 | 20 |
| app | `packages/space_admin_ui/lib/space_admin_ui.dart:207` | `build` | 20 | 20 |
| app | `client/lib/src/recovery_qr.dart:49` | `cardFromArtifact` | 20 | 19 |
| app | `client/lib/ui/recovery_panel.dart:225` | `restore` | 16 | 19 |
| app | `packages/space_admin_ui/lib/src/server_icon.dart:58` | `safeServerSvg` | 19 | 19 |
| app | `client/lib/src/core.dart:133` | `discover` | 23 | 18 |
| app | `client/lib/src/home_runtime.dart:234` | `extractVerifiedHomeArchive` | 18 | 18 |
| app | `client/lib/ui/camera_scanner.dart:58` | `start` | 17 | 18 |
| app | `client/lib/ui/chat_view.dart:181` | `send` | 17 | 18 |
| app | `client/lib/ui/recovery_panel.dart:112` | `run` | 10 | 18 |
| app | `client/lib/ui/recovery_panel.dart:356` | `revoke` | 12 | 18 |
| app | `client/lib/src/chat_controller.dart:701` | `_selectChannel` | 18 | 17 |
| app | `client/lib/src/home_controller.dart:191` | `_load` | 18 | 17 |
| app | `client/lib/ui/pairing_panel.dart:72` | `poll` | 17 | 17 |
| app | `client/lib/ui/recovery_file_picker.dart:120` | `build` | 11 | 17 |
| app | `client/lib/src/camera_qr.dart:7` | `cameraCardInWorker` | 22 | 16 |
| app | `client/lib/src/chat_controller.dart:318` | `unreadCount` | 14 | 16 |
| app | `client/lib/src/chat_controller.dart:904` | `_watchCatalog` | 9 | 16 |
| app | `client/lib/src/core.dart:53` | `parseConnectionInput` | 16 | 16 |
| app | `client/lib/src/root_rotation.dart:186` | `finishRootRotation` | 23 | 16 |
| app | `client/lib/ui/server_inbox.dart:56` | `build` | 13 | 16 |
| app | `packages/space_admin_ui/lib/src/control_spacing.dart:32` | `spaceControls` | 13 | 16 |
| server | `internal/postgres/rotation.go:141` | `CompleteRootRotation` | 40 | 37 |
| server | `internal/postgres/recovery.go:102` | `createRecoveryChallenge` | 28 | 30 |
| server | `internal/postgres/auth.go:241` | `login` | 18 | 28 |
| server | `internal/postgres/channels.go:484` | `UpdateChannelAccess` | 21 | 28 |
| server | `internal/postgres/rotation.go:45` | `CreateRootRotation` | 30 | 28 |
| server | `internal/postgres/auth.go:164` | `register` | 26 | 27 |
| server | `internal/postgres/channels.go:91` | `channelAccess` | 24 | 26 |
| server | `internal/postgres/channels.go:167` | `channelList` | 17 | 25 |
| server | `internal/home/backup.go:114` | `inventory` | 14 | 24 |
| server | `internal/postgres/subscribe.go:12` | `Subscribe` | 15 | 24 |
| server | `internal/home/runtime.go:58` | `StartDatabase` | 17 | 22 |
| server | `internal/postgres/auth.go:97` | `CompleteChallenge` | 22 | 22 |
| server | `internal/postgres/channels.go:296` | `UpdateChannel` | 22 | 21 |
| server | `cmd/space-server/main.go:40` | `run` | 20 | 20 |
| server | `internal/postgres/recovery.go:180` | `completeRecovery` | 21 | 20 |
| server | `internal/postgres/admin.go:181` | `UpdateSettings` | 22 | 19 |
| server | `internal/postgres/compatibility.go:23` | `CheckCompatibility` | 16 | 19 |
| server | `cmd/space-demo/main.go:43` | `run` | 25 | 18 |
| server | `internal/postgres/pairing.go:118` | `PollPairing` | 10 | 18 |
| server | `internal/home/certificate.go:20` | `Certificate` | 14 | 17 |
| server | `internal/postgres/channels.go:364` | `DeleteChannel` | 17 | 17 |
| server | `internal/postgres/server_icon.go:15` | `validateServerIcon` | 17 | 17 |
| server | `internal/transport/http.go:25` | `HandlerWithEndpoint` | 13 | 17 |
| server | `internal/transport/origin.go:17` | `restrictOrigin` | 14 | 16 |

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

## Проверка изменений

На итоговых исходниках выполнены `flutter analyze --no-pub` (без замечаний), `flutter test --no-pub` (236 успешных, 4 пропуска), `flutter build windows --release --no-pub`, hot reload (без ошибок Dart), `go test ./...`, `go vet ./...` и 9 тестов Python для упаковки. Для выделенных серверных операций добавлены проверки origin, конфликтующих служебных команд и замены файла состояния. Перенос на Bloc дополнительно проверен семью тестами событий, состояний, порядка и закрытия. Интеграционные сценарии PostgreSQL прошли на отдельной временной базе PostgreSQL 17.11; новый прогон CI не выполнялся.

Изменения находятся в рабочих каталогах приложения и сервера, без публикации новой версии.

## Повторная проверка читаемости

Shell.build: COG 47 → 0; его рабочая область, нижняя навигация и адаптивная компоновка имеют COG 5, 1 и 4. CreateChallenge: 52 → 10. safeSVG: 31 → 7. run: 55 → 33. Это изменения конкретных функций, а не утверждение об отсутствии сложности во всём проекте.

Добавлены проверки вариантов запросов авторизации, границ SVG и запрета публичных запросов до назначения владельца. Проверены реальный запуск demo сервера, discovery Connect, healthz и завершение через managed stdin. Тесты PostgreSQL использовали только новую временную базу, без обращения к домашним данным. Ошибки AXTree исправлены удалением вложенного Tooltip у кнопки сервера. Регрессионный тест проверяет достижимость обновляемых узлов дерева доступности; описание локального воспроизведения и проверки Windows находится в READABILITY.md репозитория приложения. Новый CI не запускался.

## Сверка отчёта после исправления подсказок

Повторно выполнены анализ Flutter, все 236 тестов (4 штатных пропуска), сборка Windows Release, go test ./... и go vet ./.... Численный аудит перед следующим этапом: приложение — 1444 рабочие функции, 48 превышений COG > 15; сервер — 244 функции, 25 превышений. Новый тест имеет COG 12, serverTooltip — 4. Интеграционный прогон с временной PostgreSQL и ручной сценарий Windows UI Automation относятся к предыдущим проверкам; при этой сверке они повторно не выполнялись.

## Следующий этап упрощения

Мастер createOwnSpace: COG 37 → 10; чтение настроек, сохранение и переход к управлению выделены отдельно (COG 2, 9 и 3). HomePanel.build: 36 → 11; ошибки, обновление, ссылка подключения и кнопка запуска имеют отдельные методы (COG 2, 6, 0 и 10). Серверный run: 33 → 20; подготовка домашнего runtime и проверка адресов выделены отдельно (COG 8 и 2). Порядок запуска и закрытия ресурсов сохранён.

В приложении осталось 46 рабочих функций с COG > 15, в сервере — 25. Это промежуточный результат. Добавлены тесты допустимых/недопустимых адресов и обязательного home-data. Реальный demo сервер вернул healthz 204 и завершился с кодом 0 после закрытия управляющего stdin. Полные проверки Go и анализ Flutter прошли; итоговые результаты тестов и сборки указаны в READABILITY.md приложения.

## Выбор runtime и резервное копирование

HomeInstallation.active: COG 32 → 6. Чтение выбранного выпуска, поиск установок и проверка marker имеют COG 2, 9 и 4. Проверки каталога, платформы, контрольной суммы и единственного кандидата сохранены. CreateBackup: COG 28 → 12; копирование конфигурации и запись манифеста имеют COG 9 и 2. Проверки остановки базы остаются до копирования, после копирования и для подготовленной копии.

Текущий итог: 45 рабочих функций приложения и 24 сервера с COG > 15. Анализ Flutter без замечаний; 238 тестов прошли, 4 штатно пропущены. Go test/vet прошли. TestStoppedDatabaseBackupAndRestore выполнен без пропуска на отдельной временной PostgreSQL с установленным runtime v0.2.2: проверены запрет копирования работающей базы, восстановление данных, конфигурации, идентичности и сертификата. Пользовательская база не использовалась. Hot reload Windows debug успешен. Изменения локальные.

Сборка Windows Release на итоговых исходниках завершена успешно.
