#!/bin/sh
export PATH=/bin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
for module in /modules/*.ko; do insmod "$module"; done
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  [ -e /dev/hvc0 ] && break
  sleep 1
done
exec </dev/hvc0 >/dev/hvc0 2>&1
set -eu
trap 'echo P1_GUEST_FAILED; poweroff -f' EXIT
mount -t tmpfs tmpfs /tmp
chmod 1777 /tmp
mount -t cgroup2 none /sys/fs/cgroup
echo '+memory +pids +cpu' > /sys/fs/cgroup/cgroup.subtree_control
mkdir /sys/fs/cgroup/p1
echo '+memory +pids +cpu' > /sys/fs/cgroup/p1/cgroup.subtree_control
export P1_CGROUP=/sys/fs/cgroup/p1 P1_WORKER=/p1-worker P1_PRESSURE=/p1-pressure
cd /suite
echo P1_GUEST_READY
uname -a
cat /proc/meminfo
./isolation.test -test.v -test.run '^TestIsolation' -test.timeout 180s
rmdir /sys/fs/cgroup/p1
echo P1_GUEST_SUCCESS
trap - EXIT
poweroff -f
