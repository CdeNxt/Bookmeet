#!/bin/sh
set -e

mkdir -p /data

# A private door for the owner (staging): join their Tailscale network and serve the app
# there over HTTPS, at https://<TAILSCALE_HOSTNAME>.<their tailnet>.ts.net. Runs only in
# an image built with TAILSCALE=1 and given TAILSCALE_AUTHKEY. Everything here is in the
# background and never fatal: the app starts the same whether or not the network answers.
# The node's identity is kept on the data volume, so the key is spent once, on first start.
if [ -n "${TAILSCALE_AUTHKEY:-}" ] && command -v tailscaled >/dev/null 2>&1; then
    mkdir -p /data/tailscale
    # Userspace networking: no TUN device or firewall rules needed, and inbound "serve"
    # traffic is all this is for.
    tailscaled --tun=userspace-networking --statedir=/data/tailscale 2>&1 \
        | sed 's/^/tailscaled: /' &
    (
        n=0
        until tailscale status --json >/dev/null 2>&1 || [ "$n" -ge 20 ]; do
            n=$((n + 1)); sleep 1
        done
        if tailscale up --auth-key="$TAILSCALE_AUTHKEY" \
                --hostname="${TAILSCALE_HOSTNAME:-bookmeet-staging}" --timeout=60s \
            && tailscale serve --bg "http://127.0.0.1:${PORT:-3000}" >/dev/null; then
            echo "tailscale: serving the app on the tailnet"
        else
            echo "tailscale: could not join the tailnet; the app runs without it" >&2
        fi
    ) &
fi

if [ -n "$LITESTREAM_REPLICA_URL" ]; then
    # Restore the latest snapshot only if the DB file doesn't already exist.
    # -if-replica-exists prevents an error when the replica bucket is empty
    # (e.g. very first deploy).
    if [ ! -f /data/calnode.db ]; then
        litestream restore \
            -config /etc/litestream.yml \
            -if-replica-exists \
            /data/calnode.db
    fi

    # Start calnode under Litestream so WAL changes are streamed continuously.
    exec litestream replicate \
        -config /etc/litestream.yml \
        -exec "/calnode"
else
    # No replica configured — run directly (local dev or first-time setup).
    exec /calnode
fi
