# Migrating from nerostr v1

nerostr v1 (the `v0.0.x` series) ran three services: the `nerostr` paywall, the `strfry` relay, and a strfry plugin that asked the paywall who may publish.
v2 is a single service. Paid members and events carry over, and v1's files stay in place, so you can roll back.
v1's documentation stays at the [`v0.0.71` tag](https://github.com/pluja/nerostr/tree/v0.0.71).

- [Before you start](#before-you-start)
- [With the migration script (recommended)](#with-the-migration-script-recommended)
- [By hand](#by-hand)
- [Rolling back](#rolling-back)
- [Good to know](#good-to-know)

## Before you start

v2 is not an image update. Until you migrate, pin the v1 images in your v1 `docker-compose.yml`, so an automatic update can't replace them:

```yaml
image: pluja/nerostr:v0.0.71
image: pluja/nerostr-strfry@sha256:1ab3e92e7559783e83eeea4926d87d9331026462dad029030c8fa7e7a5d4a19d
```

You need:

- Docker with the Compose plugin, as a user in the `docker` group (or with `sudo`).
- About 3 GB of free disk, plus twice the size of your strfry database (`nerostr_data/strfry/`).
- v1 running, since its events are exported while it serves.
- A few minutes of downtime, at the end.

This guide assumes v1 is in `~/nerostr` (with `nerostr_data/` in it) and puts v2 in `~/nerostr-v2`. Use your own paths if they differ.

Consider moving to a view-only wallet at the same time. v2 only needs to see incoming payments,
so with a view-only wallet your funds can't be spent even if the server is compromised. The script can set one up for you.

## With the migration script (recommended)

`migrate-v1.sh` migrates while v1 keeps serving, and asks before the short switch-over.
Run it from a new directory next to your v1 one, which becomes the v2 install:

```bash
mkdir ~/nerostr-v2 && cd ~/nerostr-v2 &&
curl -fsSL --remote-name-all https://github.com/pluja/nerostr/releases/latest/download/{docker-compose.yml,example.env,migrate-v1.sh} &&
bash migrate-v1.sh ~/nerostr
```

Large relays take a while to import (about 30 minutes per million events), so run it inside `tmux` or `screen`.
To check the script before running it, leave out the last line, run `gh attestation verify migrate-v1.sh --repo pluja/nerostr`, then `bash migrate-v1.sh ~/nerostr`.

The script goes through six steps:

1. **Checking.** It reads your v1 settings, pulls the v2 images, and shows what it found before asking to continue.
2. **Preparing v2.** It checks that your Monero node answers, and offers a public one if it doesn't.
   It offers to give v2 a view-only copy of your wallet, opens the wallet to check its password (asking you if v1's `.env` has the wrong one), and writes v2's `.env`.
3. **Exporting events** from v1, which keeps serving.
4. **Importing events** into v2. The import re-verifies every signature.
5. **Switching.** It asks first. The default answer is no, so you can stop here and come back later.
   Then it stops v1's paywall, so v1 takes no new events or signups, exports what was posted since step 3, and stops v1.
   It imports those events, migrates the paid members, makes sure v2's new invoices can't reuse a subaddress v1 handed out, and starts v2.
6. **Checking v2.** It waits until v2 is healthy and its wallet reaches the Monero node.

If a step fails, fix the cause and run the script again: it continues where it stopped.
If you started v1 again in the meantime, it redoes the switch, so the events and members from that time come along.
Its logs and event exports are in `~/nerostr-v2/migration/`.

When it's done:

- Point your TLS reverse proxy at `127.0.0.1:8080`, for the paywall and the relay alike, since strfry is gone.
  There are examples for [Caddy](../deploy/Caddyfile), [nginx](../deploy/nginx.conf) and [caddy-docker-proxy](../deploy/caddy-docker-proxy.override.yml).
- Check `https://your.domain/healthz`.
- Review `.env` for the relay name, admins and alerts (see [Configuration](../README.md#configuration)).
- If you chose a view-only wallet, delete the v1 wallet in `~/nerostr/nerostr_data/wallet/`, which holds your spend key,
  once you won't roll back and your seed is backed up offline.
- Delete `~/nerostr-v2/migration/` once you're happy.

## By hand

These are the steps the script automates, except the switch to a view-only wallet (step 4 explains why).

1. **Export events** while v1 keeps running:

   ```bash
   cd ~/nerostr
   date +%s > export-started
   docker exec nerostr-strfry-relay /app/strfry export > events.jsonl
   ```

2. **Prepare v2** next to it:

   ```bash
   mkdir ~/nerostr-v2 && cd ~/nerostr-v2 &&
   curl -fsSL --remote-name-all https://github.com/pluja/nerostr/releases/latest/download/{docker-compose.yml,example.env} &&
   cp example.env .env && mkdir -p data/nerostr data/wallet && sudo chown -R 1000:1000 data
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
   docker compose run --rm --no-deps -v ~/nerostr/events.jsonl:/import/events.jsonl:ro nerostr import /import/events.jsonl
   ```

   Keep `--no-deps`: both installs are the Compose project `nerostr`, so starting v2's wallet would replace v1's running one.

4. **Stop v1, import what changed meanwhile, and migrate members and the wallet:**

   ```bash
   cd ~/nerostr
   docker stop nerostr-paywall
   docker exec nerostr-strfry-relay /app/strfry export --since=$(( $(cat export-started) - 86400 )) > events-delta.jsonl
   docker exec nerostr-monero-wallet-rpc curl -s http://127.0.0.1:18083/json_rpc -d '{"jsonrpc":"2.0","id":0,"method":"store"}'
   docker compose down
   cd ~/nerostr-v2
   docker compose run --rm --no-deps -v ~/nerostr/events-delta.jsonl:/import/events.jsonl:ro nerostr import /import/events.jsonl
   sudo cp -a ~/nerostr/nerostr_data/nerostr/nerostr-db ~/nerostr-v2/v1db
   sudo chown -R 1000:1000 ~/nerostr-v2/v1db
   docker compose run --rm --no-deps -v ~/nerostr-v2/v1db:/v1db:ro nerostr migrate-v1 /v1db
   sudo cp ~/nerostr/nerostr_data/wallet/<wallet> ~/nerostr/nerostr_data/wallet/<wallet>.keys data/wallet/
   printf '%s' '<MONERO_WALLET_PASSWORD from the v1 .env>' | sudo install -m 600 -o 1000 -g 1000 /dev/stdin data/wallet/password
   sudo chown -R 1000:1000 data/wallet
   ```

   Stopping the paywall first makes v1 refuse new events and signups, so nothing posted before the shutdown is missed.
   The delta export reaches back a day before the first export, because events can be signed well before they reach the relay.

   v1 ran as root, so only root can read its database; the copy gives it to uid 1000, which v2 runs as. `migrate-v1` copies every paid user and never writes to the database it reads.
   v1 does not close its database when stopped, so `migrate-v1` recovers a temporary copy, which needs about 2 GB of free disk while it runs. Once v2 is running, you can delete `~/nerostr-v2/v1db`.

   The wallet is copied only after `store` saved it, so v2 continues after the last subaddress v1 handed out.
   A new invoice that reused one of v1's subaddresses could be marked paid by a late payment to the old v1 invoice.
   For the same reason, switching to a new view-only wallet is left to the script, which moves it past v1's subaddresses.

5. **Start v2:**

   ```bash
   docker compose up -d
   ```

   Then point your reverse proxy at it and go through the checklist at the end of [the script section](#with-the-migration-script-recommended).

## Rolling back

v1's files stay in place, so you can go back at any point:

```bash
cd ~/nerostr-v2 && docker compose down
cd ~/nerostr && docker compose up -d
```

Members who joined on v2 in the meantime are not in v1. To migrate again with the script, run it again: it notices that v1 is running and redoes the switch.

## Good to know

- Unpaid v1 invoices are not carried over. Payments to them still reach your wallet, and you can grant those users access with `allowpubkey` (see [Administration](../README.md#administration)).
- The v1 strfry relay accepted events from members only, so imported events need no further filtering.
- v2 requires an `API_KEY` of at least 16 characters. If yours was shorter, set a new one to keep using the v1 API.
