#!/bin/sh
get_fstype() {
  awk -v target="$1" '
    $5 == target { line = $0 }
    END {
      if (line == "") exit 1
      split(line, parts, " - ")
      split(parts[2], fs, " ")
      print fs[1]
    }
  ' /proc/self/mountinfo
}

echo "Waiting for /apps squashfs mount from apps-mounter sidecar..."
until [ "$(get_fstype /apps || true)" = "squashfs" ]; do
  echo "Current /apps fstype: $(get_fstype /apps || echo none)"
  sleep 1
done

echo "/apps is mounted as squashfs"
ls -la /apps | head || true

exec "$@"
