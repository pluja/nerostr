# nerostr: a nostr relay paid with Monero

Everyone can read. Only people who paid a small one-time fee in Monero can publish.

- Try it: https://xmr.usenostr.org
- Add it to your relay list: `wss://xmr.usenostr.org`

## Contents

- [Features](#features)
- [How it works](#how-it-works)
- [Self-hosting](#self-hosting)
- [Configuration](#configuration)
- [Administration](#administration)
- [Backups](#backups)
- [Upgrading](#upgrading)
- [Migrating from nerostr v1 (strfry)](#migrating-from-nerostr-v1-strfry)
- [Development](#development)
- [Known nerostr relays](#known-nerostr-relays)

## Features

- **One binary, one port.** Relay, paywall and admin API in a single Go program built on
  [khatru](https://khatru.nostr.technology/), with [LMDB](https://www.symas.com/lmdb) for events and SQLite for members.
- **Private payments.** Every invoice gets a fresh Monero subaddress. A view-only wallet is enough, so the server never holds your spend key.
- **Payments that don't get lost.** Partial payments add up, and payments that arrive after an invoice expired are still credited (for 30 days by default).
- **Standard nostr integration.**
  - [NIP-11](https://nips.nostr.com/11) advertises the admission fee and payment page, so clients can show it.
  - [NIP-86](https://nips.nostr.com/86) lets you manage members, bans and blocked IPs from any NIP-86 client, authenticated with your nostr key.
  - Also supported: NIP-09 deletions, NIP-40 expiration, NIP-42 auth, NIP-45 counts, NIP-70 protected events and NIP-77 negentropy sync.
- **Hardened by default.**
  - Per-IP and per-key rate limits, with trusted-proxy aware client IPs.
  - A strict Content-Security-Policy.
  - Non-root, read-only containers.
  - The wallet RPC sits on an internal network behind a password.
- **A simple UI.** No JavaScript required. With JavaScript you also get live payment status, copy buttons and login with a NIP-07 browser extension.

## How it works

1. A user enters their public key (npub or hex) on the relay's web page.
2. nerostr asks `monero-wallet-rpc` for a new subaddress and shows it with the amount and a QR code.
3. Every 20 seconds nerostr checks the wallet for transfers to open invoices.
   Once the received amount has enough confirmations (`MIN_CONFIRMATIONS`, default 1), the key becomes a member.
4. Members can publish. Everyone else gets `restricted: pay the one-time admission fee at https://…` from the relay, which most clients show to the user.

Reading is always free. Former members can still delete their own events.

## Self-hosting

You need a small Linux server with Docker, a domain, and a Monero wallet.

1. Get the files:

   ```bash
   git clone https://github.com/pluja/nerostr && cd nerostr
   cp example.env .env
   mkdir -p data/nerostr data/wallet
   ```

2. Put a wallet in `data/wallet/`. **Use a view-only wallet**: it can create subaddresses and see incoming payments, but cannot spend.
   You can create one from your main wallet's address and private view key, for example with
   `monero-wallet-cli --generate-from-view-key` or [Feather Wallet](https://featherwallet.org/).
   You need `<name>` and `<name>.keys`, plus the password in a file:

   ```bash
   cp /path/to/nerostr_wallet /path/to/nerostr_wallet.keys data/wallet/
   printf '%s' 'your wallet password' > data/wallet/password
   chmod 600 data/wallet/password
   sudo chown -R 1000:1000 data          # both containers run as uid 1000
   ```

3. Edit `.env`. At least set `PUBLIC_URL`, `MONERO_WALLET_FILENAME`, `MONERO_DAEMON_ADDRESS`, and a random `MONERO_WALLET_RPC_PASSWORD` (`openssl rand -hex 32`).
   Every option is documented in [`example.env`](example.env).

4. Start it:

   ```bash
   docker compose up -d
   docker compose logs -f nerostr
   ```

5. Put a TLS reverse proxy in front of `127.0.0.1:8080`. Web pages, websockets, NIP-11 and NIP-86 all go to the same upstream:

   - [Caddy](deploy/Caddyfile): `reverse_proxy 127.0.0.1:8080`
   - [nginx](deploy/nginx.conf)
   - [caddy-docker-proxy](deploy/caddy-docker-proxy.override.yml): copy it to `docker-compose.override.yml` and set your domain.

Check `https://your.domain/healthz`. It reports the database status, and whether the payment monitor can reach the wallet.

### Choosing a Monero node

Your own `monerod` is best for privacy and reliability. A pruned node needs about 100 GB of disk.
A remote node works, but it learns which transactions your wallet is interested in.
Pick a reputable one; [xmrlist.com](https://xmrlist.com/) and [monero.fail](https://monero.fail/) list public nodes.

## Configuration

Configuration is read from environment variables (`.env` with Docker Compose).

| Variable | Default | Meaning |
|---|---|---|
| `PUBLIC_URL` | (required) | Public origin, e.g. `https://xmr.example.com`. `NEROSTR_HOST=<host>` also works. |
| `ADMISSION_AMOUNT` | `0.002` | One-time fee in XMR. |
| `MIN_CONFIRMATIONS` | `1` | Confirmations before access is granted. `0` accepts mempool payments. |
| `INVOICE_EXPIRE_TIME` | `3600` | Seconds before an invoice is replaced by a new one. |
| `LATE_PAYMENT_WINDOW` | `2592000` | Seconds during which payments to expired invoices still count. |
| `ADMIN_PUBKEYS` | | Comma-separated npubs allowed to use the NIP-86 management API. |
| `RELAY_NAME`, `RELAY_DESCRIPTION`, `RELAY_PUBKEY`, `RELAY_CONTACT`, `RELAY_ICON` | | NIP-11 relay information. |
| `MONERO_WALLET_RPC_URL` | `http://monero-wallet-rpc:18083/json_rpc` | Wallet RPC endpoint. |
| `MONERO_WALLET_RPC_USER`, `MONERO_WALLET_RPC_PASSWORD` | | Digest auth for the wallet RPC (set by the compose file). |
| `MAX_OPEN_INVOICES` | `500` | Cap on unpaid, unexpired invoices. |
| `PAYMENT_POLL_INTERVAL` | `20` | Seconds between wallet checks. |
| `TRUSTED_PROXIES` | private ranges | Sources whose `X-Forwarded-For` is trusted. |
| `API_KEY` | | Enables the v1 `X-API-KEY` endpoints (see below). |
| `DATA_DIR`, `LISTEN_ADDR`, `LOG_LEVEL` | `/data`, `:8080`, `info` | |

## Administration

### NIP-86 (recommended)

Add your npub to `ADMIN_PUBKEYS` and restart. Then use any NIP-86 client, or [`nak`](https://github.com/fiatjaf/nak):

```bash
nak admin allowpubkey        --sec <your nsec> --pubkey <hex> --reason "friend" wss://xmr.example.com
nak admin banpubkey          --sec <your nsec> --pubkey <hex> --reason "spam" wss://xmr.example.com
nak admin listallowedpubkeys --sec <your nsec> wss://xmr.example.com
```

| Method | Effect |
|---|---|
| `allowpubkey` / `unallowpubkey` / `listallowedpubkeys` | Grant, revoke or list membership. Allowing a key also lifts its ban. |
| `banpubkey` / `unbanpubkey` / `listbannedpubkeys` | A ban revokes membership, blocks publishing (even after paying) and deletes the key's events. |
| `banevent` / `allowevent` / `listbannedevents` | Delete an event and refuse it if it is sent again. |
| `blockip` / `unblockip` / `listblockedips` | Refuse websocket connections from an IP. |

### v1 HTTP API

If `API_KEY` is set, the v1 endpoints keep working:

```bash
curl -X POST   -H "X-API-KEY: $API_KEY" https://xmr.example.com/api/user/<npub or hex>
curl -X DELETE -H "X-API-KEY: $API_KEY" https://xmr.example.com/api/user/<npub or hex>
```

`GET /api/status/<pubkey>` returns `{"action":"accept"}` for members and `{"action":"reject"}` otherwise.
`GET /api/user/<pubkey>` returns membership and invoice status as JSON.

## Backups

Everything lives in `./data`:

| Path | What | Back up? |
|---|---|---|
| `data/wallet/` | Wallet files and password | **Yes.** Keep your seed offline as well. |
| `data/nerostr/nerostr.db` | Members, invoices, bans (SQLite) | **Yes.** nerostr also writes a daily snapshot to `data/nerostr/backups/` (7 kept). |
| `data/nerostr/events/` | Events (LMDB) | Optional; stop nerostr first, or copy it with `mdb_copy`. |

If you ever restore the wallet from its seed, set the subaddress lookahead above the number of invoices you've created.
Otherwise old subaddresses won't be detected.

## Upgrading

```bash
git pull
docker compose pull      # or: docker compose build
docker compose up -d
```

The Monero version is pinned in [`wallet-rpc/Dockerfile`](wallet-rpc/Dockerfile), with checksums from the signed `hashes.txt`.
Upgrade it promptly when a new Monero release is out. The FCMP++ hard fork, in particular, will require one.

## Migrating from nerostr v1 (strfry)

v2 replaces the `nerostr` + `strfry` + plugin setup with a single service. Members and events carry over, and v1's files are never modified.
The steps assume your v1 directory is `~/nerostr` (with `nerostr_data/`) and v2 goes to `~/nerostr-v2`.

1. **Export events** while v1 keeps running:

   ```bash
   cd ~/nerostr
   date +%s > export-started
   docker exec nerostr-strfry-relay /app/strfry export > events.jsonl
   ```

2. **Prepare v2** next to it, reusing the same wallet:

   ```bash
   git clone https://github.com/pluja/nerostr ~/nerostr-v2 && cd ~/nerostr-v2
   mkdir -p data/nerostr data/wallet
   cp ~/nerostr/nerostr_data/wallet/<wallet> ~/nerostr/nerostr_data/wallet/<wallet>.keys data/wallet/
   printf '%s' '<MONERO_WALLET_PASSWORD from the v1 .env>' > data/wallet/password && chmod 600 data/wallet/password
   sudo chown -R 1000:1000 data
   cp example.env .env
   ```

   In `.env`:
   - set `PUBLIC_URL` to `https://` plus your v1 `NEROSTR_HOST`;
   - set `MONERO_DAEMON_ADDRESS` to your v1 `MONERO_DAEMON_RPC_HOSTNAME:MONERO_DAEMON_RPC_PORT`;
   - copy `MONERO_WALLET_FILENAME`, `ADMISSION_AMOUNT` and `INVOICE_EXPIRE_TIME`;
   - set a new `MONERO_WALLET_RPC_PASSWORD`;
   - keep `API_KEY` if you used the v1 API.

3. **Import events** while v1 is still serving. It is safe to repeat (duplicates are skipped) and runs at roughly 600 events/s, so a large relay takes a while:

   ```bash
   docker compose run --rm -v ~/nerostr/events.jsonl:/import/events.jsonl:ro nerostr import /import/events.jsonl
   ```

4. **Stop v1, import what changed meanwhile, and migrate members:**

   ```bash
   cd ~/nerostr
   docker exec nerostr-strfry-relay /app/strfry export --since=$(( $(cat export-started) - 600 )) > events-delta.jsonl
   docker compose down
   cd ~/nerostr-v2
   docker compose run --rm -v ~/nerostr/events-delta.jsonl:/import/events.jsonl:ro nerostr import /import/events.jsonl
   docker compose run --rm -v ~/nerostr/nerostr_data/nerostr/nerostr-db:/v1db:ro nerostr migrate-v1 /v1db
   ```

   `migrate-v1` copies every paid user. If v1 was not shut down cleanly, it recovers a temporary copy and leaves the original alone.

5. **Start v2** and point your reverse proxy at it (for caddy-docker-proxy, use [the override](deploy/caddy-docker-proxy.override.yml)):

   ```bash
   docker compose up -d
   ```

To roll back, run `docker compose down` in `~/nerostr-v2` and `docker compose up -d` in `~/nerostr`.

Two v1 leftovers:
- Unpaid v1 invoices are not carried over. Payments to them still reach your wallet, and you can grant those users access with `allowpubkey`.
- The v1 strfry relay accepted events from members only, so imported events need no further filtering.

## Development

```bash
go test -race ./...
PUBLIC_URL=http://localhost:8080 DATA_DIR=./data/dev go run .
```

The code is small:
- `internal/relay`: khatru setup, write policy and NIP-86;
- `internal/payments`: invoices and the payment monitor;
- `internal/web`: pages and HTTP API;
- `internal/store`: SQLite;
- `internal/wallet`: wallet RPC client.

## 🧡 Support this project

[kycnot.me/about#support](https://kycnot.me/about#support)

## Known nerostr relays

| Relay | Paywall | Status and info |
|---|---|---|
| wss://xmr.usenostr.org | [xmr.usenostr.org](https://xmr.usenostr.org) | [info](https://nostr.watch/relay/xmr.usenostr.org) |
| wss://nostr.portemonero.com | [nostr.portemonero.com](https://nostr.portemonero.com) | [info](https://nostr.watch/relay/nostr.portemonero.com) |
| wss://xmr.ithurtswhenip.ee | [xmr.ithurtswhenip.ee](https://xmr.ithurtswhenip.ee) | [info](https://nostr.watch/relay/xmr.ithurtswhenip.ee) |
| wss://nostr.xmr.rocks | [nostr.xmr.rocks](https://nostr.xmr.rocks) | [info](https://nostr.watch/relay/nostr.xmr.rocks) |
