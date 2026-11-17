#!/bin/sh
# BusyBox crond launches jobs in separate sessions. Forward shutdown explicitly
# rather than assuming an init process-group signal reaches active Go workers.
set -eu
rm -f /tmp/collection-stopping
cron_pid=
shutdown() {
    trap '' TERM INT
    touch /tmp/collection-stopping
    if [ -n "$cron_pid" ]; then
        kill -TERM "$cron_pid" 2>/dev/null || true
        wait "$cron_pid" 2>/dev/null || true
    fi
    killall -TERM collection 2>/dev/null || true
    # Go cancels HTTP and performs a bounded flush; Docker's 75s is the outer bound.
    remaining=60
    while killall -0 collection 2>/dev/null && [ "$remaining" -gt 0 ]; do
        sleep 1
        remaining=$((remaining - 1))
    done
    exit 0
}
trap shutdown TERM INT
/usr/sbin/crond -f -l 5 -L /dev/stdout &
cron_pid=$!
wait "$cron_pid"
