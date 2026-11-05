#!/bin/sh
# Environment contains credentials; never enable shell tracing or echo it.
set -eu
bin=${COLLECTION_BIN:-/app/collection}
groups=${COLLECTION_GROUPS:-/app/configs/groups.json}
case "${1:-}" in
  tick)
    case "${2:-}" in 08|12|16|20) hour=$2 ;; *) exit 2 ;; esac
    slot="$(TZ=Europe/Moscow date +%Y-%m-%d)T${hour}:00:00+03:00"
    exec "$bin" tick --groups "$groups" --slot "$slot"
    ;;
  minute)
    # Recover a missed trigger (today only); retries re-use the same run IDs.
    if ! "$bin" plan --due --groups "$groups" --execution-timeout=45s; then
      echo 'collection: due-slot planning incomplete; next minute will retry' >&2
    fi
    exec "$bin" resume --groups "$groups"
    ;;
  *) exit 2 ;;
esac
