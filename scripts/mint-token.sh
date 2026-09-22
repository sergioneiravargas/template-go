#!/usr/bin/env bash
# mint-token.sh - mint a local RS256 JWT signed with private.pem for manual testing.
#
# Usage:
#   ./scripts/mint-token.sh [private.pem] [sub] [ttl-seconds]
#   export TOKEN=$(./scripts/mint-token.sh private.pem <user-uuid>)
#
# The token's signature is accepted by auth.Middleware (local PEM public key),
# but the sub must be the id of an existing auth_user row - register a user via
# POST /api/v1/auth/register first and pass its id as sub.

set -euo pipefail

KEY="${1:-private.pem}"
SUB="${2:-user-1}"
TTL="${3:-3600}"

if [ ! -r "$KEY" ]; then
	echo "cannot read private key: $KEY" >&2
	exit 1
fi

b64url() {
	openssl base64 -A | tr '+/' '-_' | tr -d '='
}

now=$(date +%s)
exp=$((now + TTL))

header=$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)
payload=$(printf '{"sub":"%s","email":"%s@example.com","iat":%d,"exp":%d}' "$SUB" "$SUB" "$now" "$exp" | b64url)
sig=$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -sign "$KEY" | b64url)

printf '%s.%s.%s\n' "$header" "$payload" "$sig"
