# Keeper

Watches Dead Man's Switch vaults, nudges owners before their deadlines, and serves a
read-only index of what it sees.

**It never signs a transaction.** `claim` is permissionless and `check_in` belongs to the
owner, so there is nothing a keeper would ever need to send — and therefore no private key
it needs to hold. The protocol is correct whether or not this process is running; the
keeper only solves the UX problem the protocol creates: *I forgot to check in and lost my
assets.*

## What it does

```
RPC node ──GetProgramAccounts──► decode ──► cache (memory | Postgres) ──► HTTP index
                                    │
                                    └──► thresholds ──► reminders (log, Telegram)
```

1. **Scan.** Every `DMS_SCAN_INTERVAL`, fetch every account the program owns, filtered by
   size and by the Anchor discriminator, and decode it.
2. **Cache.** Replace the stored set wholesale. A vault missing from a scan has been
   closed, so it leaves the index too.
3. **Remind.** For each vault, work out how long the owner has left and send at most one
   message per threshold crossed.
4. **Serve.** Answer queries from the cache, never from RPC.

## Running it

```bash
go run ./cmd/keeper
```

With no environment at all it watches devnet, caches in memory and logs reminders — enough
to see it work. Everything is configurable:

| Variable | Default | Meaning |
| --- | --- | --- |
| `DMS_RPC_ENDPOINT` | `https://api.devnet.solana.com` | RPC node to read from |
| `DMS_PROGRAM_ID` | the devnet program | Program whose vaults to watch |
| `DMS_SCAN_INTERVAL` | `5m` | How often to scan |
| `DMS_REMIND_AT` | `336h,168h,24h,1h` | Warn this far ahead of a deadline, one message each |
| `DMS_LISTEN_ADDR` | `:8080` | HTTP address for the index |
| `DMS_POSTGRES_DSN` | — | Set to persist the cache; unset means memory |
| `DMS_TELEGRAM_TOKEN` | — | Bot token; set with the chat id or neither |
| `DMS_TELEGRAM_CHAT_ID` | — | Where to send reminders |

## HTTP index

| Route | Returns |
| --- | --- |
| `GET /healthz` | liveness plus the number of cached vaults |
| `GET /v1/vaults` | every vault, with status and time left |
| `GET /v1/vaults?status=expired` | only vaults in that status |
| `GET /v1/vaults/{address}` | one vault |
| `GET /v1/owners/{owner}/vaults` | one owner's vaults |

```json
{
  "address": "…",
  "owner": "…",
  "asset": "SOL",
  "status": "due_soon",
  "deadline": "2027-02-14T12:00:00Z",
  "seconds_left": 864000,
  "beneficiaries": [{ "address": "…", "share_bps": 7000, "share_percent": 70, "claimed": false }]
}
```

Statuses are `active`, `due_soon`, `expired` and `triggered`. The loud one is **`expired`**:
heirs can claim right now, but none has yet — the owner has not lost the vault, they are
one transaction away from it.

## Three things worth reading the code for

**The decoder** (`internal/dms/vault.go`) reads the account layout by hand rather than
through generated bindings. It matters that it reads *sequentially*: `Option<Pubkey>`
serialises to one byte when the mint is absent and thirty-three when it is present, so a
fixed-offset decoder would silently misread every SOL vault. `vault_test.go` builds account
bytes by hand — not with the decoder's own helpers — and asserts on every field of both
shapes.

**The reminder tracker** (`internal/watch/reminder.go`) deduplicates per vault *and* per
threshold, and treats a moved deadline as a reset: a check-in proves the owner is alive and
re-arms every threshold. A keeper that was offline for a week sends one message on
recovery, not four.

**The store** (`internal/store`) takes a whole scan rather than individual upserts, which
is what makes closed vaults disappear from the index instead of lingering as stale rows.

## `dmsctl`

The other binary here, and the opposite of the keeper: it signs. Kept in a separate
package (`internal/client`) that nothing under `internal/watch` imports, so the daemon
cannot grow a key by accident.

```bash
go build -o dmsctl ./cmd/dmsctl

./dmsctl open     --id 1 --timeout 30d --heir <pubkey>:7000 --heir <pubkey>:3000
./dmsctl deposit  --id 1 --sol 0.2
./dmsctl check-in --id 1
./dmsctl status              # every vault the program owns
./dmsctl claim    --vault <address> --wallet heir.json
./dmsctl close    --id 1
```

Instruction discriminators come from the generated IDL rather than from a hash recomputed
here, so a rename in the program surfaces as a failing transaction against a stale
constant instead of a silently different instruction.

## `blink` — the same vault as a link

The third binary: a [Solana Action](https://solana.com/docs/advanced/actions) server. A
client fetches it with GET to learn what can be done, POSTs the user's public key, and gets
back an **unsigned** transaction to hand to that user's wallet. Like the keeper, it holds
no keys.

```bash
go build -o blink ./cmd/blink && ./blink
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `DMS_BLINK_LISTEN_ADDR` | `:8081` | listen address |
| `DMS_BLINK_BASE_URL` | `http://localhost:8081` | this server's **public** origin |
| `DMS_CLUSTER` | `devnet` | picks the CAIP-2 chain id in `X-Blockchain-Ids` |

`DMS_BLINK_BASE_URL` matters more than it looks: icons must be absolute URLs, and a server
behind a tunnel or a proxy cannot infer its own public address from the request. Point it
at whatever the outside world sees.

A blink also has to be reachable over public HTTPS — a client cannot fetch `localhost`. For
a demo, run a tunnel and set the base URL to the address it hands you, then share:

```
https://dial.to/?action=solana-action:<base-url>/api/actions/check-in
```

### Hosting it

The repository root carries a `Dockerfile` plus configuration for two hosts:
[`fly.toml`](../fly.toml) and [`render.yaml`](../render.yaml). Koyeb needs no config file —
point it at the repository and it builds the same Dockerfile.

Nothing needs to be set per host. The server binds `PORT` when the platform injects one,
and reads its public origin from the forwarded headers, so the same image runs on any of
them, behind a tunnel, or on a laptop.

Two things to know before choosing. Fly asks for a payment method before it will create an
app at all, free tier or not. A free Render service sleeps after about fifteen minutes idle
and takes tens of seconds to wake, which for a blink reads as broken. Koyeb's free tier
asks for neither and does not sleep.

Two details the spec is unforgiving about, both of which fail silently:

- **CORS.** Missing headers mean the browser blocks the request before this server ever
  sees it, and the card simply never appears.
- **Signature slots.** The transaction is unsigned but must still carry one zeroed
  signature slot per required signer; a wallet reads the count from the message header and
  rejects bytes that leave them out.

## Tests

```bash
go test ./...
go test -race ./...
```

55 tests, no network and no database: the chain sits behind interfaces and the cache
defaults to memory. Three decode golden account bytes written by the Anchor program's own
serializer — the only check that the hand-written decoder agrees with the program rather
than with a mirror of the same assumptions. The blink tests decode the transaction the
server returns and assert on its fee payer, its program and its empty signature slot.
