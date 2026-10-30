#!/bin/bash
set -euo pipefail
# Выполняется только на одноразовом Ubuntu runner GitHub CI.
sudo apt-get update
sudo apt-get install -y --no-install-recommends linux-image-generic busybox-static cpio zstd wabt
python3 fixtures.py
mkdir -p guest/{bin,dev,proc,sys,tmp,modules,suite/testdata} vm-image
cp /bin/busybox guest/bin/
for app in sh mount chmod insmod mkdir cat uname rmdir poweroff sleep; do ln -s busybox "guest/bin/$app"; done
kernel=$(find /boot -maxdepth 1 -name 'vmlinuz-*-generic' | sort -V | tail -1)
version=${kernel##*/vmlinuz-}
sudo cp "$kernel" vm-image/vmlinuz
sudo chmod 644 vm-image/vmlinuz
# Сохраняем порядок зависимостей; BusyBox не требует базы depmod внутри гостя.
index=0
for module in $({ modprobe --set-version "$version" --show-depends virtio_console; modprobe --set-version "$version" --show-depends virtio_pci; } | awk '$1 == "insmod" {print $2}' | awk '!seen[$0]++'); do
  index=$((index+1))
  target=$(printf 'guest/modules/%02d.ko' "$index")
  case "$module" in
    *.zst) zstd -d -c "$module" > "$target" ;;
    *.xz) xz -d -c "$module" > "$target" ;;
    *) cp "$module" "$target" ;;
  esac
done
CGO_ENABLED=0 go build -o guest/p1-worker ./cmd/p1-worker
CGO_ENABLED=0 go build -o guest/p1-pressure ./cmd/p1-pressure
CGO_ENABLED=0 go test -c -o guest/suite/isolation.test .
cp testdata/*.wasm guest/suite/testdata/
cp macos/guest-init.sh guest/init
chmod +x guest/init
(cd guest && find . -print0 | cpio --null -o --format=newc --owner=0:0 | gzip -n) > vm-image/initramfs.gz
dpkg-query -W linux-image-generic busybox-static > vm-image/packages.txt
cp "/boot/config-$version" vm-image/kernel-config.txt
(cd vm-image && sha256sum vmlinuz initramfs.gz packages.txt kernel-config.txt > SHA256SUMS)
