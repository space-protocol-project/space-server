#!/bin/sh
# Linux x64: проверенный runtime и собственная PostgreSQL, без отдельной СУБД.
set -eu
umask 077

fail() { printf '%s\n' "$*" >&2; exit 1; }
[ "$(id -u)" -ne 0 ] || fail 'Запускайте от обычного пользователя: PostgreSQL не работает от root.'
[ "$(uname -s)/$(uname -m)" = 'Linux/x86_64' ] || fail 'Этот установщик рассчитан на Linux x64.'
for tool in curl unzip sha256sum mktemp; do
  command -v "$tool" >/dev/null 2>&1 || fail "Нужна команда: $tool"
done
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
printf '\nДанные: %s/home\nОткройте входящий TCP 8443 в файрволе. Ctrl+C штатно остановит сервер.\n' "$base"
rm -rf -- "$work"
trap - EXIT HUP INT TERM
exec "$runtime/space-server" start -data "$base/home" "$@"
