#!/bin/sh
# Linux x64: проверенный runtime и собственная PostgreSQL, без отдельной СУБД.
set -eu
umask 077

fail() { printf '%s\n' "$*" >&2; exit 1; }
service=false
case "${1:-}" in
  --service) service=true; shift ;;
  --foreground) shift ;;
esac
[ "$(id -u)" -ne 0 ] || fail 'Запускайте от обычного пользователя: PostgreSQL не работает от root.'
[ "$(uname -s)/$(uname -m)" = 'Linux/x86_64' ] || fail 'Этот установщик рассчитан на Linux x64.'
for tool in curl unzip sha256sum mktemp; do
  command -v "$tool" >/dev/null 2>&1 || fail "Нужна команда: $tool"
done
if "$service"; then
  for tool in systemctl loginctl sed flock; do
    command -v "$tool" >/dev/null 2>&1 || fail "Для службы нужна команда: $tool"
  done
  systemctl --user show-environment >/dev/null 2>&1 || fail 'Нет пользовательского systemd. Войдите по SSH обычным пользователем (не через su) и повторите команду.'
  if [ "$(loginctl show-user "$(id -un)" -p Linger --value)" != yes ]; then
    loginctl --no-ask-password enable-linger "$(id -un)" || fail 'Нужен автозапуск без входа: выполните sudo loginctl enable-linger "$USER", затем повторите установку.'
  fi
  [ "$(loginctl show-user "$(id -un)" -p Linger --value)" = yes ] || fail 'Не удалось включить автозапуск пользователя.'
  for argument in "$@"; do
    case "$argument" in
      -managed*|--managed*|-data|--data|-data=*|--data=*) fail 'Служба сама управляет каталогом данных и временем жизни процесса.' ;;
    esac
  done
fi
base=${SPACE_HOME:-"$HOME/.local/share/space-server"}
version=${SPACE_VERSION:-latest}
repository=https://github.com/space-protocol-project/space-server
if [ "$version" = latest ]; then
  release=$(curl --proto '=https' --tlsv1.2 -fsSL -o /dev/null -w '%{url_effective}' "$repository/releases/latest")
  version=${release##*/}
fi
case "$version" in v[0-9]*.[0-9]*.[0-9]*) ;; *) fail 'Некорректная версия runtime' ;; esac
case "$version" in *[!a-zA-Z0-9._-]*) fail 'Некорректная версия runtime' ;; esac
mkdir -p "$base/runtimes"
base=$(cd "$base" && pwd -P)
if "$service"; then
  mkdir -p "$HOME/.local/share/space-server-service"
  exec 9>"$HOME/.local/share/space-server-service/install.lock"
  flock -n 9 || fail 'Установка службы уже выполняется.'
fi
work=$(mktemp -d "$base/.download.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT HUP INT TERM
asset=Space-home-linux-x64.zip
url=$repository/releases/download/$version
curl --proto '=https' --tlsv1.2 -fSL "$url/$asset" -o "$work/$asset"
curl --proto '=https' --tlsv1.2 -fSL "$url/SHA256SUMS.txt" -o "$work/SHA256SUMS.txt"
(cd "$work"; sha256sum --ignore-missing --check SHA256SUMS.txt) || fail 'Контрольная сумма не совпала'
mkdir "$work/runtime"
unzip -q "$work/$asset" -d "$work/runtime"
chmod +x "$work/runtime/space-server" "$work/runtime/postgres/bin/"*
info=$("$work/runtime/space-server" -runtime-info)
printf '%s' "$info" | grep -Eq '"remoteSetupVersion"[[:space:]]*:[[:space:]]*1' || fail 'Этот выпуск ещё не поддерживает первый вход из приложения. Нужен новый runtime.'
runtime=$base/runtimes/$version
if [ ! -d "$runtime" ]; then mv "$work/runtime" "$runtime"; fi
if "$service"; then
  # Shell-аргументы сохраняются как литералы, без eval и раскрытия переменных.
  quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }
  launcher=$HOME/.local/share/space-server-service/run
  unit=$HOME/.config/systemd/user/space-server.service
  mkdir -p "$(dirname "$launcher")" "$(dirname "$unit")"
  chmod 700 "$(dirname "$launcher")"
  # Приватный stdout: одноразовая ссылка никогда не попадает в journal.
  setup=$base/setup.txt
  [ ! -L "$setup" ] || fail 'Файл ссылки не должен быть символической ссылкой.'
  (umask 077; : > "$setup")
  chmod 600 "$setup"
  {
    printf '#!/bin/sh\nset -eu\numask 077\nexec '
    quote "$runtime/space-server"
    printf ' start -data '; quote "$base/home"
    for argument in "$@"; do printf ' '; quote "$argument"; done
    printf ' > '; quote "$setup"; printf '\n'
  } > "$work/run"
  chmod 700 "$work/run"
  cat > "$work/space-server.service" <<'UNIT'
[Unit]
Description=Space Server
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
Type=exec
ExecStart="%h/.local/share/space-server-service/run"
Restart=on-failure
RestartSec=5
KillMode=mixed
TimeoutStopSec=60
UMask=0077
NoNewPrivileges=yes
RestrictSUIDSGID=yes
StandardOutput=null
StandardError=journal

[Install]
WantedBy=default.target
UNIT
  if [ "$(systemctl --user show space-server.service -p LoadState --value 2>/dev/null || true)" != not-found ]; then
    systemctl --user stop space-server.service
  fi
  mv "$work/run" "$launcher"
  mv "$work/space-server.service" "$unit"
  systemctl --user daemon-reload
  touch "$work/starting"
  systemctl --user enable --now space-server.service
  attempts=0
  while [ ! "$base/home/data/state.json" -nt "$work/starting" ] || { [ -f "$base/home/data/bootstrap-pending" ] && [ ! -s "$setup" ]; }; do
    attempts=$((attempts + 1))
    [ "$attempts" -lt 90 ] || fail 'Сервер не готов за 90 секунд. Проверьте: journalctl --user -u space-server.service -n 50'
    [ "$(systemctl --user show space-server.service -p ActiveState --value)" != failed ] || fail 'Служба завершилась с ошибкой. Проверьте: journalctl --user -u space-server.service -n 50'
    sleep 1
  done
  printf '\nСервер запущен как служба и переживёт выход из SSH и перезагрузку.\nДанные: %s/home\n' "$base"
  cat "$setup"
  printf '\nСтатус: systemctl --user status space-server\nПерезапуск: systemctl --user restart space-server\nОстановка: systemctl --user stop space-server\nЖурнал: journalctl --user -u space-server -n 50\n'
  exit 0
fi
printf '\nДанные: %s/home\nОткройте входящий TCP 8443 в файрволе. Ctrl+C штатно остановит сервер.\n' "$base"
rm -rf -- "$work"
trap - EXIT HUP INT TERM
exec "$runtime/space-server" start -data "$base/home" "$@"
