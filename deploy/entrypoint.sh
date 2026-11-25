#!/bin/sh
set -eu
if [ "$#" -eq 0 ]; then
    set -- serve -data /var/lib/nekoport -listen "${NEKOPORT_LISTEN_ADDRESS:-0.0.0.0:8080}" -master-key-file /var/lib/nekoport/master.key
fi
exec /usr/local/bin/nekoport "$@"
