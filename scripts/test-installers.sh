#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
FIXTURE_DIR=$(mktemp -d)
trap 'rm -rf "$FIXTURE_DIR"' EXIT HUP INT TERM

fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

assert_file() {
  [ -f "$1" ] || fail "expected file: $1"
}

assert_not_exists() {
  [ ! -e "$1" ] || fail "unexpected path: $1"
}

ARCHIVE_DIR="$FIXTURE_DIR/archive"
MOCK_BIN="$FIXTURE_DIR/bin"
mkdir -p "$ARCHIVE_DIR" "$MOCK_BIN"
printf '#!/bin/sh\nprintf "radar fixture %%s\\n" "$*"\n' > "$ARCHIVE_DIR/kubectl-radar"
chmod 0755 "$ARCHIVE_DIR/kubectl-radar"
tar -czf "$FIXTURE_DIR/radar_v0.0.0_linux_amd64.tar.gz" -C "$ARCHIVE_DIR" kubectl-radar
ARCHIVE_SHA=$(sha256sum "$FIXTURE_DIR/radar_v0.0.0_linux_amd64.tar.gz" | awk '{print $1}')
export FIXTURE_DIR ARCHIVE_SHA

cat > "$MOCK_BIN/curl" <<'EOF'
#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output=$2; shift 2 ;;
    -w) shift 2 ;;
    https://*) url=$1; shift ;;
    *) shift ;;
  esac
done

latest='https://github.com/maomada/radar/releases/latest'
tag='https://github.com/maomada/radar/releases/tag/v0.0.0'
prefix='https://github.com/maomada/radar/releases/download/v0.0.0/'
case "$url" in
  "$latest")
    case "${MOCK_MODE:-owned}" in
      upstream) printf '%s' 'https://github.com/skyhook-io/radar/releases/tag/v0.0.0' ;;
      unknown) printf '%s' 'https://example.com/releases/tag/v0.0.0' ;;
      *) printf '%s' "$tag" ;;
    esac
    ;;
  "$prefix"*)
    filename=${url#"$prefix"}
    case "$filename" in
      checksums.txt)
        case "${CHECKSUM_CASE:-valid}" in
          valid) printf '%s  %s\n' "$ARCHIVE_SHA" "radar_v0.0.0_linux_amd64.tar.gz" > "$output" ;;
          mismatch) printf '%064d  %s\n' 0 "radar_v0.0.0_linux_amd64.tar.gz" > "$output" ;;
          missing) printf '%s  %s\n' "$ARCHIVE_SHA" "other.tar.gz" > "$output" ;;
          *) exit 1 ;;
        esac
        ;;
      radar_v0.0.0_linux_amd64.tar.gz)
        cp "$FIXTURE_DIR/radar_v0.0.0_linux_amd64.tar.gz" "$output"
        ;;
      *) exit 1 ;;
    esac
    ;;
  *)
    printf 'unexpected URL: %s\n' "$url" >&2
    exit 1
    ;;
esac
EOF
chmod 0755 "$MOCK_BIN/curl"

run_install() {
  install_dir=$1
  env PATH="$MOCK_BIN:$PATH" INSTALL_DIR="$install_dir" sh "$ROOT/install.sh"
}

default_dir="$FIXTURE_DIR/default/bin"
custom_dir="$FIXTURE_DIR/custom/bin"
run_install "$default_dir" >/dev/null
assert_file "$default_dir/kubectl-radar"
[ -L "$default_dir/radar" ] || fail "default fixture did not create radar alias"
"$default_dir/radar" --version >/dev/null

run_install "$custom_dir" >/dev/null
assert_file "$custom_dir/kubectl-radar"
[ -L "$custom_dir/radar" ] || fail "custom fixture did not create radar alias"
"$custom_dir/kubectl-radar" --version >/dev/null

for checksum_case in mismatch missing; do
  rejected_dir="$FIXTURE_DIR/$checksum_case"
  if CHECKSUM_CASE="$checksum_case" run_install "$rejected_dir" > "$FIXTURE_DIR/$checksum_case.log" 2>&1; then
    fail "$checksum_case checksum unexpectedly succeeded"
  fi
  assert_not_exists "$rejected_dir"
done
grep -Fq 'Checksum verification failed for' "$FIXTURE_DIR/mismatch.log"
grep -Fq 'Failed to find checksum for' "$FIXTURE_DIR/missing.log"

for redirect_case in upstream unknown; do
  rejected_dir="$FIXTURE_DIR/$redirect_case"
  if MOCK_MODE="$redirect_case" run_install "$rejected_dir" > "$FIXTURE_DIR/$redirect_case.log" 2>&1; then
    fail "$redirect_case redirect unexpectedly succeeded"
  fi
  assert_not_exists "$rejected_dir"
  grep -Fq 'unexpected redirect URL' "$FIXTURE_DIR/$redirect_case.log"
done

relative_dir="$FIXTURE_DIR/relative"
if (cd "$FIXTURE_DIR" && env PATH="$MOCK_BIN:$PATH" INSTALL_DIR=relative/bin sh "$ROOT/install.sh" > "$FIXTURE_DIR/relative.log" 2>&1); then
  fail "relative INSTALL_DIR unexpectedly succeeded"
fi
assert_not_exists "$relative_dir"
grep -Fq 'INSTALL_DIR must be an absolute path' "$FIXTURE_DIR/relative.log"

grep -Fq "INSTALL_DIR=\"\${INSTALL_DIR:-/usr/local/bin}\"" "$ROOT/install.sh"
if grep -Fq 'api.github.com' "$ROOT/install.sh" "$ROOT/install.ps1"; then
  fail 'installers must not use api.github.com for discovery'
fi

printf '%s\n' 'POSIX installer fixtures passed'
