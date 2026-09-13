#!/bin/sh
set -eu

fail() {
  # Diagnostics are deliberately short: inputs may be hostile.
  printf '%s\n' "consumer-shell: invalid or incompatible evidence" >&2
  exit 2
}

[ "$#" -ge 1 ] || fail
mode=$1
shift
case "$mode:$#" in
  consume:2) ;;
  progress:3) ;;
  *) fail ;;
esac

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || fail
for input do
  [ -f "$input" ] || fail
  bytes=$(LC_ALL=C wc -c <"$input") || fail
  [ "$bytes" -le 4194304 ] || fail
  # jq replaces malformed UTF-8, which would destroy exact key/framing evidence.
  iconv -f UTF-8 -t UTF-8 "$input" >/dev/null 2>&1 || fail
done

if [ "$mode" = consume ]; then
  jq -n -r --rawfile capture "$1" --rawfile expected "$2" \
    --arg previous '' --arg current '' \
    -f "$here/consumer.jq" --arg operation consume 2>/dev/null || fail
else
  jq -n -r --rawfile previous "$1" --rawfile current "$2" \
    --rawfile expected "$3" --arg capture '' \
    -f "$here/consumer.jq" --arg operation progress \
    2>/dev/null || fail
fi
