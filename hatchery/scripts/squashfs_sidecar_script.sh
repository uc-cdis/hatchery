#!/bin/sh
SRC="${SOURCE_SQSH:-/image/apps-current.sqsh}"
CACHE_DIR="/sqsh-cache"
TARGET="/apps"

mkdir -p "$CACHE_DIR" "$TARGET"

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

echo "Checking source SquashFS: $SRC"

if [ ! -f "$SRC" ]; then
  echo "ERROR: SquashFS source not found: $SRC" >&2
  exit 1
fi

if [ -n "${EXPECTED_SHA256:-}" ]; then
  CACHE="$CACHE_DIR/${EXPECTED_SHA256}.sqsh"
else
  CACHE="$CACHE_DIR/apps-current.sqsh"
fi

if [ ! -f "$CACHE" ]; then
  echo "Copying SquashFS from $SRC to local cache $CACHE"

  rm -f "$CACHE.tmp"
  cp "$SRC" "$CACHE.tmp"

  if [ -n "${EXPECTED_SHA256:-}" ]; then
    echo "${EXPECTED_SHA256}  $CACHE.tmp" | sha256sum -c -
  else
    echo "WARNING: EXPECTED_SHA256 is empty; skipping digest verification" >&2
  fi

  mv "$CACHE.tmp" "$CACHE"
else
  echo "Using existing local SquashFS cache: $CACHE"
fi

if [ -n "${EXPECTED_SHA256:-}" ]; then
  echo "${EXPECTED_SHA256}  $CACHE" | sha256sum -c -
fi

CURRENT_FSTYPE="$(get_fstype "$TARGET" || true)"
echo "Current filesystem at $TARGET: ${CURRENT_FSTYPE:-none}"

if [ "$CURRENT_FSTYPE" != "squashfs" ]; then
  echo "Mounting SquashFS $CACHE at $TARGET"
  mount -t squashfs -o loop,ro,nodev,nosuid "$CACHE" "$TARGET"
else
  echo "$TARGET is already mounted as squashfs"
fi

NEW_FSTYPE="$(get_fstype "$TARGET" || true)"
echo "Filesystem at $TARGET after mount: ${NEW_FSTYPE:-none}"

if [ "$NEW_FSTYPE" != "squashfs" ]; then
  echo "ERROR: expected $TARGET to be squashfs, got ${NEW_FSTYPE:-none}" >&2
  awk '$5 == "/apps" {print}' /proc/self/mountinfo || true
  exit 1
fi

echo "Apps SquashFS mounted successfully"
ls -la "$TARGET" | head || true

cleanup() {
  echo "Unmounting $TARGET"
  umount "$TARGET" || true
}

trap cleanup TERM INT EXIT

while true; do
  sleep 3600 &
  wait $!
done
