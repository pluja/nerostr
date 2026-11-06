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
- [Monitoring and alerts](#monitoring-and-alerts)
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
- **Members stay reachable.** Anyone can reply to, react to, zap and DM a member here, while the relay's feed shows members' posts only.
- **A welcome DM** to every new member, from the relay's own key (customizable).
- **Alerts on nostr.** The relay DMs you when payments stop being processed (wallet unreachable or stuck) and when they recover. An optional heartbeat URL catches full outages.
- **Standard nostr integration.**
  - [NIP-11](https://nips.nostr.com/11) advertises the admission fee and payment page, so clients can show it.
  - [NIP-86](https://nips.nostr.com/86) lets you manage members, bans and blocked IPs from any NIP-86 client, authenticated with your nostr key.
  - Also supported: NIP-09 deletions, NIP-40 expiration, NIP-42 auth, NIP-45 counts, NIP-70 protected events and NIP-77 negentropy sync.
- **Hardened by default.**
  - Per-IP and per-key rate limits, with trusted-proxy aware client IPs.
  - A flood guard that stops a looping bot or bridge from reposting the same note.
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

Top-level notes that repeat the author's own text are refused with `duplicate:`. A repeat means the same text was
seen less than an hour before, counting refused copies too, so a stuck bot stays blocked until it pauses.
Replies are never affected, so answering many people with the same "Good morning!" is fine.

### Inbox

With `INBOX=true` (the default), non-members can also publish events that tag a member, as long as the kind is in `INBOX_KINDS`.
The defaults cover replies, legacy and NIP-17 DMs, reposts, reactions, comments and zap receipts.
That way members can be reached here without a separate inbox relay. Members can list this relay in their NIP-17 DM relay list (kind 10050).

To keep the paid feed clean, events from non-members are only returned to queries that ask for something specific:
an `ids` or `authors` filter, or a `#p`, `#e`, `#a` or `#q` tag (notifications, threads, DMs).
They never appear in general feed queries or live feed subscriptions.
Inbox events are rate-limited per IP and may tag at most 20 people.

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

Check `https://your.domain/healthz`. It reports the database status, and whether payments are being processed.
Then set up [alerts](#monitoring-and-alerts), so you hear about problems before your users do.

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
| `INBOX`, `INBOX_KINDS` | `true`, `1,4,6,7,16,1059,1111,9735` | Accept events from non-members that tag a member ([Inbox](#inbox)). |
| `ALERT_PUBKEYS`, `ALERT_AFTER`, `ALERT_RELAYS` | admins, `600`, popular relays | Who gets alert DMs, after how many seconds of trouble, and where to find their DM relays. |
| `HEARTBEAT_URL`, `HEARTBEAT_INTERVAL` | , `300` | URL requested periodically while healthy (dead-man switch). |
| `WELCOME_MESSAGE` | a short welcome | DMed to every key that just paid, from the relay's own key. Supports `{name}`, `{relay}` and `{url}`; `off` disables it. |
| `RELAY_SECRET_KEY` | generated | The relay's own nostr key (signs alerts, NIP-11 `self`). Kept in `data/nerostr/relay.key` if not set. |
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

## Monitoring and alerts

nerostr watches its own payment processing. It counts as broken when the wallet RPC can't be reached,
or when the wallet height hasn't moved for 30 minutes (usually its Monero daemon is down or not syncing).

- **Nostr DMs.** After `ALERT_AFTER` seconds of trouble (10 minutes by default), the relay sends an encrypted NIP-17 DM to each `ALERT_PUBKEYS` (default: `ADMIN_PUBKEYS`).
  It sends another DM when payments recover. DMs go to the recipient's DM relays (kind 10050) and are stored on this relay too.
  They come from the relay's own key: the `self` pubkey in NIP-11, also printed at startup.
- **Heartbeat.** Set `HEARTBEAT_URL` to a push-monitor URL ([healthchecks.io](https://healthchecks.io), Uptime Kuma push monitors, …).
  nerostr requests it every `HEARTBEAT_INTERVAL` seconds while healthy, so the monitor alerts you when pings stop.
  That includes the whole server being down, which nothing inside the relay can report.
- **`/healthz`** returns the same status as JSON, for any HTTP monitor.

## Backups

Everything lives in `./data`:

| Path | What | Back up? |
|---|---|---|
| `data/wallet/` | Wallet files and password | **Yes.** Keep your seed offline as well. |
| `data/nerostr/nerostr.db` | Members, invoices, bans (SQLite) | **Yes.** nerostr also writes a daily snapshot to `data/nerostr/backups/` (7 kept). |
| `data/nerostr/events/` | Events (LMDB) | Optional; stop nerostr first, or copy it with `mdb_copy`. |
| `data/nerostr/relay.key` | The relay's own nostr key | Yes, unless you set `RELAY_SECRET_KEY`. A lost key means alerts come from a new identity. |

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
v1 is the `v0.0.x` series; its documentation stays at the [`v0.0.71` tag](https://github.com/pluja/nerostr/tree/v0.0.71).

v2 is a migration, not an image update. Until you migrate, pin the v1 images in your v1 `docker-compose.yml`:

```yaml
image: pluja/nerostr:v0.0.71
image: pluja/nerostr-strfry@sha256:1ab3e92e7559783e83eeea4926d87d9331026462dad029030c8fa7e7a5d4a19d
```

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

3. **Import events** while v1 is still serving. The import re-verifies every signature and applies the same flood rule as the relay, so the first copy of each repeated note is kept.
   It is safe to repeat (duplicates are skipped). xmr.usenostr.org's 933k events took about 30 minutes, ending at 392k after dropping a 541k-note bridge loop from July 2024:

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

You need Go and, for the Dockerfile and workflow audits, Docker or Podman.

```bash
make check     # everything CI runs: tidy, formatting, lint, tests, vulnerabilities, workflow and Dockerfile checks
make fmt       # format Go code (gofmt + goimports)
PUBLIC_URL=http://localhost:8080 DATA_DIR=./data/dev go run .
```

The linters are configured in [`.golangci.yml`](.golangci.yml) and [`.hadolint.yaml`](.hadolint.yaml).
Their versions are pinned in the [`Makefile`](Makefile), which CI also uses, so local and CI results match.
A `//nolint` comment must name the linter and say why.

The code is small:
- `internal/relay`: khatru setup, write policy and NIP-86;
- `internal/payments`: invoices and the payment monitor;
- `internal/web`: pages and HTTP API;
- `internal/store`: SQLite;
- `internal/wallet`: wallet RPC client;
- `internal/flood`: the repeated-note rule;
- `internal/alerts`: alert DMs and heartbeat.

### CI and releases

| Workflow | Runs on | Does |
|---|---|---|
| [`ci`](.github/workflows/ci.yml) | pushes to `main`, pull requests | `make check`, then builds the images (amd64 and arm64) and smoke-tests the container. |
| [`release`](.github/workflows/release.yml) | `v*` tags | Runs `ci`, then publishes the multi-arch image, static Linux binaries with `SHA256SUMS`, and a GitHub release with generated notes. |
| [`wallet-rpc`](.github/workflows/wallet-rpc.yml) | changes to `wallet-rpc/`, weekly, manually | Publishes the wallet image, tagged with its Monero version and `latest`. The weekly run picks up Debian security fixes. |

Images are published to `ghcr.io/<owner>/` with the workflow's own token, so no registry secrets are needed.
Every image has an SBOM and a signed build-provenance attestation; binaries are attested too.
Release builds never use the build cache.

One-time setup in the GitHub repository settings:

1. **Environment `release`**, which the publishing jobs run in. Allow only `main` and tags matching `v*` to deploy to it.
2. **A tag ruleset for `v*`**, so only maintainers can create, move or delete release tags.
3. **After the first publish**, open each package (`nerostr`, `nerostr-wallet-rpc`) under your profile's *Packages*.
   Set its visibility to public: new packages on user accounts start out private.

To release:

```bash
git tag -s v2.0.0 -m v2.0.0 && git push origin v2.0.0
```

A tag with a suffix, such as `v2.0.0-rc.1`, becomes a pre-release. It doesn't move `latest` or the `2` and `2.0` image tags, so it is safe for trying out a release.
To verify a published image or binary:

```bash
gh attestation verify oci://ghcr.io/pluja/nerostr:2.0.0 --owner pluja
gh attestation verify nerostr_v2.0.0_linux_amd64.tar.gz --owner pluja
```

## 🧡 Support this project

[kycnot.me/about#support](https://kycnot.me/about#support)

## Known nerostr relays

| Relay | Paywall | Status and info |
|---|---|---|
| wss://xmr.usenostr.org | [xmr.usenostr.org](https://xmr.usenostr.org) | [info](https://nostr.watch/relay/xmr.usenostr.org) |
| wss://nostr.portemonero.com | [nostr.portemonero.com](https://nostr.portemonero.com) | [info](https://nostr.watch/relay/nostr.portemonero.com) |
| wss://xmr.ithurtswhenip.ee | [xmr.ithurtswhenip.ee](https://xmr.ithurtswhenip.ee) | [info](https://nostr.watch/relay/xmr.ithurtswhenip.ee) |
| wss://nostr.xmr.rocks | [nostr.xmr.rocks](https://nostr.xmr.rocks) | [info](https://nostr.watch/relay/nostr.xmr.rocks) |
