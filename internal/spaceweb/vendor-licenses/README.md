# Локальные QR-библиотеки панели

Панель не загружает код из CDN. Закреплены npm qrcode-generator 2.0.4 (MIT, Kazuhiko Arase) и jsqr 1.4.0 (Apache-2.0, сохранён исходный LICENSE). У jsQR только оболочка module.exports заменена локальным ES-module экспортом; алгоритм не изменён.

Источники: https://github.com/kazuhikoarase/qrcode-generator и https://github.com/cozmo/jsQR. Лицензии здесь относятся только к этим библиотекам и не выбирают лицензию проекта Space.

SHA-256 встроенных файлов:

- qrcode.mjs: ea91d7118a5395289170da848b7c6758b996163bfbccf312591ab65a4911b7c0
- jsqr.mjs: 5209437c78f221aab85a2cdc2c1c875ff4b6f5183a025177ba8ac2b69cf01a9f
