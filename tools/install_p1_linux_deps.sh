#!/usr/bin/env bash
# Зеркала используют штатные ключи Ubuntu; ошибки индекса не игнорируются.
set -euo pipefail
options=(-o Acquire::ForceIPv4=true -o Acquire::Retries=1
  -o Acquire::http::Timeout=15 -o Acquire::https::Timeout=15
  -o APT::Update::Error-Mode=any)
for mirror in https://archive.ubuntu.com/ubuntu https://mirrors.edge.kernel.org/ubuntu; do
  printf '%s\n' "$mirror" | sudo tee /etc/apt/apt-mirrors.txt
  if timeout 120s sudo apt-get "${options[@]}" update &&
     timeout 480s sudo apt-get "${options[@]}" install -y "$@"; then
    exit 0
  fi
done
echo 'Не удалось установить зависимости из подписанных репозиториев Ubuntu' >&2
exit 1
