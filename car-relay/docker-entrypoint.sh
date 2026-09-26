#!/bin/sh
set -eu
mkdir -p /data
chown car:car /data
exec runuser -u car -- /usr/local/bin/car-relay -addr :8090 -db /data/car-relay.db
