#!/usr/bin/env bash
#
# The whole Dead Man's Switch lifecycle against devnet, start to finish:
# open a vault, fund it, watch the keeper warn, let the timer run out, and
# have both heirs claim their shares.
#
# This is what the recording in the README is made from, and it runs against
# the real deployed program — every signature it prints is on chain.
#
#   ./scripts/demo.sh
#
# Needs a funded devnet wallet at ~/.config/solana/id.json (about 0.2 SOL) and
# the Solana + Go toolchains on PATH. Set TIMEOUT to something longer than the
# default two minutes if you want to watch the middle of it.

set -euo pipefail

TIMEOUT=${TIMEOUT:-2m}
VAULT_ID=${VAULT_ID:-$(($(date +%s) % 100000))}
DEPOSIT=${DEPOSIT:-0.1}
RPC=${DMS_RPC_ENDPOINT:-https://api.devnet.solana.com}

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

bold=$'\033[1m'
dim=$'\033[2m'
cyan=$'\033[1;36m'
green=$'\033[1;32m'
reset=$'\033[0m'

step() { printf '\n%s» %s%s\n' "$cyan" "$*" "$reset"; }
note() { printf '%s  %s%s\n' "$dim" "$*" "$reset"; }
beat() { sleep "${1:-1}"; }

step "Building"
go -C keeper build -o "$WORK/dmsctl" ./cmd/dmsctl
go -C keeper build -o "$WORK/keeper" ./cmd/keeper
note "dmsctl signs; keeper only reads. Neither shares a key with the other."
beat

step "The program, live on devnet"
solana program show 9tfSr7zg9bGBfpSqqdwCACiSfAqsbdE4rNnwezFe5Ldm --url "$RPC" |
	grep -E 'Program Id|Authority|Data Length'
beat

step "Two heirs, 60% and 40%"
for name in alice bob; do
	solana-keygen new --no-bip39-passphrase --silent -o "$WORK/$name.json" >/dev/null
done
ALICE=$(solana-keygen pubkey "$WORK/alice.json")
BOB=$(solana-keygen pubkey "$WORK/bob.json")
printf '  alice  %s\n  bob    %s\n' "$ALICE" "$BOB"
note "Funding them just enough to pay their own claim fee."
solana transfer --url "$RPC" --allow-unfunded-recipient "$ALICE" 0.02 >/dev/null
solana transfer --url "$RPC" --allow-unfunded-recipient "$BOB" 0.02 >/dev/null
beat

step "Opening a vault with a $TIMEOUT timer"
"$WORK/dmsctl" open --id "$VAULT_ID" --timeout "$TIMEOUT" \
	--heir "$ALICE:6000" --heir "$BOB:4000"
beat

step "Funding it with $DEPOSIT SOL"
"$WORK/dmsctl" deposit --id "$VAULT_ID" --sol "$DEPOSIT"
beat

step "How the vault looks now"
"$WORK/dmsctl" status --id "$VAULT_ID"
beat 2

step "The keeper notices the deadline coming"
note "It signs nothing. It reads the chain and warns the owner."
timeout 24 env DMS_SCAN_INTERVAL=10s DMS_REMIND_AT=5m,1m "$WORK/keeper" 2>&1 |
	grep -E 'keeper starting|needs attention|scan complete' || true
beat

step "Waiting for the timer to run out"
VAULT=$("$WORK/dmsctl" status --id "$VAULT_ID" | awk '/^vault/ {print $2}')
# Bounded, so a flaky RPC read cannot hang the demo forever.
deadline=$((SECONDS + 1800))
while ((SECONDS < deadline)); do
	left=$("$WORK/dmsctl" status --id "$VAULT_ID" | awk -F'[()]' '/^deadline/ {print $2}' || true)
	case "$left" in *overdue*) break ;; esac
	printf '\r  %-40s' "${left:-reading the chain...}"
	sleep 10
done
printf '\r%-60s\r' ' '
note "Deadline passed. The owner can still check in — until an heir moves."
beat

step "The keeper escalates"
timeout 14 env DMS_SCAN_INTERVAL=10s "$WORK/keeper" 2>&1 |
	grep -E 'needs attention' || true
beat

step "Alice claims her 60%"
"$WORK/dmsctl" claim --vault "$VAULT" --wallet "$WORK/alice.json"
beat

step "The pool is frozen at the first claim"
"$WORK/dmsctl" status --id "$VAULT_ID"
note "Bob's 40% is measured against the original pool, not against the leftovers."
beat 2

step "Bob claims his 40%"
"$WORK/dmsctl" claim --vault "$VAULT" --wallet "$WORK/bob.json"
beat

step "Done"
"$WORK/dmsctl" status --id "$VAULT_ID"
printf '\n%s  alice  %s\n  bob    %s%s\n' "$green" \
	"$(solana balance --url "$RPC" "$ALICE")" \
	"$(solana balance --url "$RPC" "$BOB")" "$reset"
printf '\n%s  https://explorer.solana.com/address/%s?cluster=devnet%s\n\n' \
	"$bold" "$VAULT" "$reset"
