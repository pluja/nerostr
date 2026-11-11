# Migrating from nerostr v1

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

5. **Start v2** and point your reverse proxy at it (for caddy-docker-proxy, use [the override](../deploy/caddy-docker-proxy.override.yml)):

   ```bash
   docker compose up -d
   ```

To roll back, run `docker compose down` in `~/nerostr-v2` and `docker compose up -d` in `~/nerostr`.

Two v1 leftovers:
- Unpaid v1 invoices are not carried over. Payments to them still reach your wallet, and you can grant those users access with `allowpubkey`.
- The v1 strfry relay accepted events from members only, so imported events need no further filtering.

