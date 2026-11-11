#!/usr/bin/env bash
# Migrate a nerostr v1 (strfry) install to v2. The full guide, including the
# steps by hand: https://github.com/pluja/nerostr/blob/main/docs/migrate-from-v1.md
#
# Run it from a new directory next to your v1 one, with v2's compose files:
#
#   mkdir ~/nerostr-v2 && cd ~/nerostr-v2 &&
#   curl -fsSL --remote-name-all https://github.com/pluja/nerostr/releases/latest/download/{docker-compose.yml,example.env,migrate-v1.sh} &&
#   bash migrate-v1.sh ~/nerostr
#
# That directory becomes the v2 install. From a git clone, run ./deploy/migrate-v1.sh.
#
# v1 keeps serving until you confirm the switch, which takes a few minutes of
# downtime. v1's files stay in place, so you can roll back. If a step fails, fix
# the cause and run the script again: it continues where it stopped.
# shellcheck disable=SC2016 # as_root scripts expand $NAME inside the container, from -e NAME=...
set -Eeuo pipefail
umask 077

cd "$(dirname "$0")"
[ -e docker-compose.yml ] || cd .. # in a git clone, the script is in deploy/
V2=$PWD
WORK=$V2/migration
V1=${1:-$HOME/nerostr}
V1=$(cd "$V1" 2>/dev/null && pwd) || { echo "No v1 directory at ${1:-$HOME/nerostr}" >&2; exit 1; }
V1_DB=$V1/nerostr_data/nerostr/nerostr-db
V1_WALLET=$V1/nerostr_data/wallet
STRFRY=nerostr-strfry-relay
V1_PAYWALL=nerostr-paywall
WALLET_CTR=nerostr-monero-wallet-rpc
TMP_WALLET=nerostr-migrate-wallet

if [ -t 1 ]; then B=$'\e[1m' DIM=$'\e[2m' G=$'\e[32m' Y=$'\e[33m' R=$'\e[31m' N=$'\e[0m'; else B='' DIM='' G='' Y='' R='' N=''; fi
step() { printf '\n%s[%s/6] %s%s\n' "$B" "$1" "$2" "$N"; }
ok() { printf '  %s✓%s %s\n' "$G" "$N" "$*"; }
info() { printf '  %s%s%s\n' "$DIM" "$*" "$N"; }
warn() { printf '  %s!%s %s\n' "$Y" "$N" "$*"; }
die() { printf '\n  %s✗ %s%s\n' "$R" "$*" "$N" >&2; exit 1; }
ask() { # ask QUESTION Y|N: true for yes; an empty answer takes the default
  local hint='[y/N]' ans=''
  [ "$2" = Y ] && hint='[Y/n]'
  printf '  %s?%s %s %s ' "$B" "$N" "$1" "$hint"
  read -r ans || true
  case ${ans:-$2} in [Yy]*) return 0 ;; *) return 1 ;; esac
}
trap 'die "Failed at line $LINENO. Fix the cause and run the script again; it continues where it stopped."' ERR
cleanup() {
  docker rm -f "$TMP_WALLET" >/dev/null 2>&1 || true
  rm -f "$WORK/rpc.conf"
  if [ -n "${HELPER_IMAGE:-}" ] && { [ -d "$WORK/wallet-src" ] || [ -d "$WORK/v1db" ]; }; then
    as_root 'rm -rf /work/wallet-src /work/v1db' -v "$WORK:/work" || true
  fi
}
trap cleanup EXIT

done_() { [ -e "$WORK/done-$1" ]; }
mark() { touch "$WORK/done-$1"; }
compose() { COMPOSE_IGNORE_ORPHANS=1 docker compose "$@" </dev/null; }
# docker compose won't read the file until .env has the required values; this
# fills them in just to pull the images.
compose_images() { MONERO_WALLET_FILENAME=- MONERO_WALLET_RPC_PASSWORD=- MONERO_DAEMON_ADDRESS=- compose "$@"; }
# Run a shell script as root in a throwaway container, for the files Docker made
# root-owned. Extra docker arguments (-v, -e) follow the script.
as_root() { local script=$1; shift; docker run --rm --network none --user 0:0 --entrypoint /bin/sh "$@" "$HELPER_IMAGE" -c "$script" </dev/null; }
quietly() { # quietly LOG CMD...: keep CMD's output in $WORK/LOG, and show it only if CMD fails
  local log=$WORK/$1; shift
  "$@" >"$log" 2>&1 || { tail -n 20 "$log" | sed 's/^/    /'; die "'$*' failed; full output in $log"; }
}
v1_down() { (cd "$V1" && COMPOSE_IGNORE_ORPHANS=1 docker compose down </dev/null); }
running() { [ -n "$(docker ps -q --filter "name=^$1\$")" ]; }
randhex() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }
v1env() { # value of KEY in v1's .env, without quotes or a trailing comment
  local v
  v=$(grep -E "^[[:space:]]*(export[[:space:]]+)?$1=" <<<"$V1_ENV" | tail -n1) || true
  v=${v#*=}; v=${v%$'\r'}; v=${v#"${v%%[![:space:]]*}"}
  case $v in
    \"*) v=${v#\"}; v=${v%%\"*} ;;
    \'*) v=${v#\'}; v=${v%%\'*} ;;
    *) v=${v%%[[:space:]]#*}; v=${v%"${v##*[![:space:]]}"} ;;
  esac
  printf '%s' "$v"
}
v1_ctr_env() { # the environment Compose gave v1's running wallet container, which reads v1's .env
  local out dir
  out=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}{{"\n"}}{{range .Config.Env}}{{println .}}{{end}}' "$WALLET_CTR" 2>/dev/null) || return 0
  dir=$(head -n1 <<<"$out")
  [ "$(cd "$dir" 2>/dev/null && pwd -P)" = "$(cd "$V1" && pwd -P)" ] && tail -n +2 <<<"$out"
}
v1val() { # v1's setting KEY, exactly as Compose read it while v1 runs, else parsed from its .env
  if [ -n "$V1_CTR_ENV" ]; then sed -n "s/^$1=//p" <<<"$V1_CTR_ENV" | tail -n1; else v1env "$1"; fi
}
set_env() { # set or uncomment KEY=VALUE in .env.new
  local v=$2
  [[ $v != *"'"* ]] || die "The value for $1 contains a single quote, which .env can't hold here. Rename it and run the script again."
  [[ $v =~ ^[A-Za-z0-9._:/@+-]*$ ]] || v="'$v'"
  K=$1 V=$v awk 'BEGIN { k = ENVIRON["K"]; v = ENVIRON["V"] }
    !set && ($0 ~ "^" k "=" || $0 ~ "^# " k "=") { print k "=" v; set = 1; next }
    { print } END { if (!set) print k "=" v }' .env.new >.env.tmp && mv .env.tmp .env.new
}

wallet_rpc_start() { # wallet_rpc_start DIR ARGS...: an offline wallet RPC on DIR (as /w), reachable from this host only
  local dir=$1; shift
  RPC_PASS=$(randhex)
  printf 'rpc-login=migrate:%s\n' "$RPC_PASS" >"$WORK/rpc.conf"
  chmod 644 "$WORK/rpc.conf" # the container's uid must read it; $WORK itself is private
  docker rm -f "$TMP_WALLET" >/dev/null 2>&1 || true
  docker run -d --name "$TMP_WALLET" -p 127.0.0.1::18083 -v "$dir:/w" -v "$WORK/rpc.conf:/rpc.conf:ro" \
    "$HELPER_IMAGE" --config-file=/rpc.conf --offline "$@" >/dev/null
  RPC_PORT=$(docker port "$TMP_WALLET" 18083 | head -n1); RPC_PORT=${RPC_PORT##*:}
  for _ in $(seq 60); do
    curl -s -o /dev/null --digest -u "migrate:$RPC_PASS" "http://127.0.0.1:$RPC_PORT/json_rpc" && return 0
    running "$TMP_WALLET" || break
    sleep 1
  done
  docker logs --tail 5 "$TMP_WALLET" 2>&1 | sed 's/^/    /'
  die "The temporary wallet RPC did not start."
}
wallet_rpc_stop() { docker rm -f "$TMP_WALLET" >/dev/null; rm -f "$WORK/rpc.conf"; }
rpc_call() { # rpc_call METHOD JSON_PARAMS: the body goes through stdin, keeping keys out of ps
  curl -s --digest -u "migrate:$RPC_PASS" "http://127.0.0.1:$RPC_PORT/json_rpc" -H 'Content-Type: application/json' -d @- \
    <<<"{\"jsonrpc\":\"2.0\",\"id\":0,\"method\":\"$1\",\"params\":$2}" || true
}
rpc_error() { grep -m1 -o '"message": *"[^"]*"' | cut -d'"' -f4; }
rpc() { # rpc METHOD JSON_PARAMS: rpc_call that dies on errors
  local out
  out=$(rpc_call "$@")
  [ -n "$out" ] || die "The temporary wallet RPC did not answer ($1)."
  ! grep -q '"error"' <<<"$out" || die "Wallet RPC $1 failed: $(rpc_error <<<"$out")"
  printf '%s' "$out"
}
v1_wallet_rpc() { # v1's wallet RPC has no login and is reachable only from its own network
  docker exec -i "$WALLET_CTR" curl -s -m 60 http://127.0.0.1:18083/json_rpc -H 'Content-Type: application/json' -d @- \
    <<<"{\"jsonrpc\":\"2.0\",\"id\":0,\"method\":\"$1\",\"params\":$2}"
}
field() { grep -m1 -oE "\"$1\": *(\"[^\"]*\"|[0-9]+)" | sed -E 's/^[^:]*: *"?//; s/"$//'; }
json() { local s=${1//\\/\\\\}; printf '"%s"' "${s//\"/\\\"}"; }
max_index() { grep -o '"address_index": *[0-9]*' | grep -o '[0-9]*$' | sort -n | tail -n1; }
node_reachable() { # node_reachable HOST:PORT: whether v2's wallet container gets an answer from that Monero node
  local out
  out=$(docker run --rm --network "container:$WALLET_CTR" --entrypoint timeout "$HELPER_IMAGE" 20 bash -c \
    'exec 3<>"/dev/tcp/${1%:*}/${1##*:}" && printf "GET /get_height HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n" "$1" >&3 && cat <&3' _ "$1" 2>/dev/null) || true
  grep -q '"height"' <<<"$out"
}

step 1 "Checking"
command -v docker >/dev/null || die "Docker is not installed."
docker compose version >/dev/null 2>&1 || die "The Docker Compose plugin is missing ('docker compose')."
docker info >/dev/null 2>&1 || die "Cannot talk to Docker. Add your user to the docker group, or run this with sudo."
[ -e docker-compose.yml ] && [ -e example.env ] || die "Put this script next to v2's docker-compose.yml and example.env:
    curl -fsSL --remote-name-all https://github.com/pluja/nerostr/releases/latest/download/{docker-compose.yml,example.env}"
[ "$V1" != "$V2" ] || die "Run this from a new directory for v2, not from the v1 directory."
[ -e "$V1/.env" ] && [ -e "$V1_DB/MANIFEST" ] || die "$V1 does not look like a nerostr v1 install (no .env or nerostr_data/nerostr/nerostr-db)."
mkdir -p "$WORK" && chmod 700 "$WORK"
if done_ switched; then
  if ! running "$STRFRY"; then
    ok "Already migrated. Remove $WORK to start over."
    exit 0
  fi
  rm -f "$WORK/done-switched"
  info "v1 is running again, so the switch will be redone."
fi
if [ -e .env ] && ! done_ prepared; then
  die "This directory already has a .env. Run the script from a new directory, or move .env away."
fi
if running nerostr && { running "$STRFRY" || ! done_ started; }; then
  die "v2 is already running here. Stop it with 'docker compose down' first."
fi

info "Pulling the v2 images..."
compose_images pull >"$WORK/pull.log" 2>&1 || { cat "$WORK/pull.log"; die "Could not pull the v2 images."; }
# The wallet image doubles as the helper for root-owned files (it has a shell and
# GNU cp). Read it from the compose file, which won't parse before .env exists.
img=$(sed -n 's/^[[:space:]]*image:[[:space:]]*\(.*nerostr-wallet-rpc.*\)/\1/p' docker-compose.yml | head -n1)
case $img in
  '') die "No nerostr-wallet-rpc image in docker-compose.yml." ;;
  *'${'*) HELPER_IMAGE=${img%%\$\{*}${MONERO_VERSION:-$(sed -n 's/.*:-\([^}]*\)}.*/\1/p' <<<"$img")} ;;
  *) HELPER_IMAGE=$img ;;
esac

V1_ENV=$(cat "$V1/.env" 2>/dev/null || as_root 'cat /v1.env' -v "$V1/.env:/v1.env:ro")
V1_CTR_ENV=$(v1_ctr_env) || true
HOST=$(v1val NEROSTR_HOST); HOST=${HOST#*://}; HOST=${HOST%%/*}
W_NAME=$(v1val MONERO_WALLET_FILENAME)
DAEMON=$(v1val MONERO_DAEMON_RPC_HOSTNAME):$(v1val MONERO_DAEMON_RPC_PORT)
[ -n "$HOST" ] && [ -n "$W_NAME" ] && [ "$DAEMON" != : ] || die "v1's .env is missing NEROSTR_HOST, MONERO_WALLET_FILENAME or the daemon settings."
[ -e "$V1_WALLET/$W_NAME.keys" ] || die "Wallet $V1_WALLET/$W_NAME.keys not found."

if ! done_ exported; then
  running "$STRFRY" || die "v1 is not running. Start it first (cd $V1 && docker compose up -d): its events are exported while it serves."
fi
events_kb=$(du -sk "$V1/nerostr_data/strfry/strfry-db" 2>/dev/null | cut -f1) || true
need=$((${events_kb:-0} * 2 + 3 * 1024 * 1024)) # export + import, plus v1's 2 GB database log
free=$(df -Pk "$V2" | awk 'NR == 2 { print $4 }')

echo
printf '  %-12s %s\n' "v1 install" "$V1" "v2 install" "$V2" "Relay" "https://$HOST" "Wallet" "$W_NAME" "Monero node" "$DAEMON"
if [ "$free" -lt "$need" ]; then
  warn "Only $((free / 1024)) MB free here; the migration may need about $((need / 1024)) MB."
fi
echo
ask "Continue?" Y || exit 0

step 2 "Preparing v2"
if done_ prepared; then
  ok "Done earlier."
else
  daemon_height=$(curl -fsS -m 15 "http://$DAEMON/get_height" 2>/dev/null | field height) || true
  if [ -z "$daemon_height" ]; then
    default_node=$(sed -n 's/^MONERO_DAEMON_ADDRESS=//p' example.env)
    warn "The v1 Monero node $DAEMON did not answer from this machine."
    if [ "$DAEMON" != "$default_node" ] && ask "Use the public node $default_node instead?" N; then
      DAEMON=$default_node
      daemon_height=$(curl -fsS -m 15 "http://$DAEMON/get_height" 2>/dev/null | field height) || true
    else
      warn "Keeping $DAEMON. If v2's wallet can't reach it either, change MONERO_DAEMON_ADDRESS in .env."
    fi
  fi

  echo
  info "v2 only needs to see incoming payments, so it can run on a view-only wallet:"
  info "if this server is ever compromised, your funds still can't be spent."
  VIEW_ONLY=false
  ask "Give v2 a view-only copy of your v1 wallet? (recommended)" Y && VIEW_ONLY=true

  if [ -n "$V1_CTR_ENV" ]; then
    v1_wallet_rpc store '{}' 2>/dev/null | grep -q '"result"' ||
      warn "v1's wallet did not save before the copy, so v2 may take longer to sync."
  fi
  mkdir -p "$V2/data"
  as_root 'rm -rf /work/wallet-src && mkdir /work/wallet-src &&
    cp "/v1w/$NAME.keys" /work/wallet-src/ && { [ ! -e "/v1w/$NAME" ] || cp "/v1w/$NAME" /work/wallet-src/; } &&
    chown -R 1000:1000 /work/wallet-src' -e NAME="$W_NAME" -v "$V1_WALLET:/v1w:ro" -v "$WORK:/work"
  wallet_rpc_start "$WORK/wallet-src" --wallet-dir=/w
  W_PASS=$(v1val MONERO_WALLET_PASSWORD)
  until out=$(rpc_call open_wallet "{\"filename\":$(json "$W_NAME"),\"password\":$(json "$W_PASS")}") && ! grep -q '"error"' <<<"$out"; do
    grep -qi password <<<"$out" || die "Could not open the v1 wallet: $(rpc_error <<<"$out")"
    printf '  %s?%s The password from v1%ss .env does not open the wallet. Wallet password: ' "$B" "$N" "'"
    read -rs W_PASS || die "No password given."
    echo
  done
  rpc get_address '{"account_index":0}' | max_index >"$WORK/v1-copy-subaddress"

  if $VIEW_ONLY; then
    V2_NAME=$W_NAME-viewonly
    address=$(rpc get_address '{"account_index":0}' | field address)
    viewkey=$(rpc query_key '{"key_type":"view_key"}' | field key)
    if [ -n "$daemon_height" ]; then height=$((daemon_height - 720)); else height=$(rpc get_height '{}' | field height); fi
    randhex >"$WORK/password.new"
    out=$(rpc generate_from_keys "{\"filename\":$(json "$V2_NAME"),\"address\":\"$address\",\"viewkey\":\"$viewkey\",\"password\":\"$(cat "$WORK/password.new")\",\"restore_height\":$height}")
    unset viewkey
    grep -q 'Watch-only wallet' <<<"$out" || die "The wallet RPC did not create a view-only wallet: $out"
    rpc close_wallet '{}' >/dev/null
    ok "Created view-only wallet $V2_NAME for ${address:0:8}…${address: -8}, scanning from block $height"
  else
    V2_NAME=$W_NAME
    rpc close_wallet '{}' >/dev/null
    printf '%s' "$W_PASS" >"$WORK/password.new"
    ok "Copied wallet $W_NAME"
  fi
  unset W_PASS
  wallet_rpc_stop
  as_root 'mkdir -p /data/nerostr /data/wallet && mv "/work/wallet-src/$NAME.keys" /data/wallet/ &&
    { [ ! -e "/work/wallet-src/$NAME" ] || mv "/work/wallet-src/$NAME" /data/wallet/; } &&
    cp /work/password.new /data/wallet/password && chmod 600 /data/wallet/password &&
    chown -R 1000:1000 /data && rm -rf /work/wallet-src /work/password.new' -e NAME="$V2_NAME" -v "$V2/data:/data" -v "$WORK:/work"
  printf '%s' "$V2_NAME" >"$WORK/wallet-name"

  cp example.env .env.new
  set_env PUBLIC_URL "https://$HOST"
  set_env MONERO_WALLET_FILENAME "$V2_NAME"
  set_env MONERO_DAEMON_ADDRESS "$DAEMON"
  set_env MONERO_WALLET_RPC_PASSWORD "$(randhex)"
  for k in ADMISSION_AMOUNT INVOICE_EXPIRE_TIME; do
    v=$(v1val $k)
    [ -z "$v" ] || set_env $k "$v"
  done
  API_KEY=$(v1val API_KEY)
  if [ -z "$API_KEY" ]; then
    :
  elif [ ${#API_KEY} -lt 16 ]; then
    warn "v1's API_KEY is shorter than the 16 characters v2 requires; set a new one in .env to keep using the API."
  elif [[ $API_KEY == *"'"* ]]; then
    warn "v1's API_KEY contains a single quote; copy it to .env yourself to keep using the API."
  else
    set_env API_KEY "$API_KEY"
  fi
  chmod 600 .env.new && mv .env.new .env
  ok "Wrote .env (review it later for the relay name, admins and alerts)"
  mark prepared
fi

step 3 "Exporting events from v1 (v1 keeps serving)"
if done_ exported; then
  ok "Done earlier."
else
  date +%s >"$WORK/export-started"
  docker exec "$STRFRY" /app/strfry export </dev/null >"$WORK/events.jsonl" 2>"$WORK/export.log" ||
    die "strfry export failed; see $WORK/export.log"
  chmod 644 "$WORK/events.jsonl"
  ok "Exported $(wc -l <"$WORK/events.jsonl") events"
  mark exported
fi

# --no-deps matters: both installs are compose project "nerostr" with a
# monero-wallet-rpc service, so starting v2's would replace v1's running wallet.
import_events() { # import_events FILE: events already imported are skipped, so it is safe to repeat
  if ! compose run --rm --no-deps -T -v "$1:/import/events.jsonl:ro" nerostr import /import/events.jsonl 2>&1 |
    tee "$WORK/import.log" | { grep --line-buffered -E 'level=(ERROR|WARN)|import (progress|finished)' | sed -u 's/^/    /'; true; }; then
    die "The import failed; see $WORK/import.log"
  fi
}

step 4 "Importing events into v2 (v1 keeps serving)"
if done_ imported; then
  ok "Done earlier."
else
  info "This re-verifies every signature: about 30 minutes per million events."
  import_events "$WORK/events.jsonl"
  mark imported
fi

step 5 "Switching from v1 to v2"
if done_ delta && ! running "$STRFRY" &&
  [ -n "$(find "$V1/nerostr_data" -newer "$WORK/done-delta" -print -quit 2>/dev/null)" ]; then
  rm -f "$WORK"/done-{delta,delta-imported,members,subaddresses,started}
  die "v1 ran again after it was stopped for the switch. Start it (cd $V1 && docker compose up -d) and run the script again, so its new events and members come along."
fi
if running "$STRFRY"; then
  # v1 may have served again since an earlier attempt, so everything from here is redone.
  rm -f "$WORK"/done-{delta,delta-imported,members,subaddresses,started}
  info "Your relay is offline from here until v2 starts, usually a few minutes."
  ask "Stop v1 and switch to v2 now?" N || {
    echo
    info "Nothing changed. Run the script again when you're ready; it continues from here."
    exit 0
  }
  # v1's write policy asks the paywall about every event, so without it v1 takes no new events or signups.
  docker stop "$V1_PAYWALL" >/dev/null 2>&1 || true
  last=""
  for _ in $(seq 30); do
    [ -n "$V1_CTR_ENV" ] || break
    last=$(v1_wallet_rpc get_address '{"account_index":0}' 2>/dev/null | max_index) || true
    [ -z "$last" ] || break
    sleep 2 # a freshly started wallet RPC takes a moment to answer
  done
  printf '%s' "$last" >"$WORK/v1-last-subaddress"
  # A day of overlap: events can be signed well before they reach the relay.
  since=$(($(cat "$WORK/export-started") - 86400))
  docker exec "$STRFRY" /app/strfry export --since="$since" </dev/null >"$WORK/events-delta.jsonl" 2>>"$WORK/export.log"
  chmod 644 "$WORK/events-delta.jsonl"
  quietly v1-down.log v1_down
  ! running "$STRFRY" || die "v1 is still running after 'docker compose down' in $V1."
  mark delta
elif ! done_ delta; then
  die "v1 is not running, so the events posted since the export can't be fetched. Start it (cd $V1 && docker compose up -d) and run the script again."
fi
ok "Stopped v1"
if ! done_ delta-imported; then
  import_events "$WORK/events-delta.jsonl"
  mark delta-imported
fi
ok "Imported the events posted since the first export"
if ! done_ members; then
  # v1 ran as root and left its database readable only by root; v2 runs as uid 1000.
  as_root 'rm -rf /work/v1db && cp -a /v1db /work/v1db && chown -R 1000:1000 /work/v1db' -v "$V1_DB:/v1db:ro" -v "$WORK:/work"
  quietly migrate.log compose run --rm --no-deps -T -v "$WORK/v1db:/v1db:ro" nerostr migrate-v1 /v1db
  grep 'level=' "$WORK/migrate.log" | sed 's/^/    /' || true
  as_root 'rm -rf /work/v1db' -v "$WORK:/work"
  mark members
fi
ok "Migrated members"
if ! done_ subaddresses; then
  # v2 detects payments by subaddress, so new invoices must not reuse one v1 handed out.
  last=$(cat "$WORK/v1-last-subaddress")
  wallet_rpc_start "$V2/data/wallet" --wallet-file="/w/$(cat "$WORK/wallet-name")" --password-file=/w/password
  have=$(rpc get_address '{"account_index":0}' | max_index)
  [[ $have =~ ^[0-9]+$ ]] || die "Could not read the subaddresses of v2's wallet."
  # The margin covers v1 handing out more if it is rolled back to for a while.
  if [ -n "$last" ]; then
    target=$((last + 100))
  else
    copy=$(cat "$WORK/v1-copy-subaddress" 2>/dev/null) || true
    [ "${copy:-0}" -gt "$have" ] && target=$copy || target=$have
    target=$((target + 1000))
    warn "Could not read v1's last subaddress, so v2 skips 1000 to be safe."
  fi
  while [ "$have" -lt "$target" ]; do
    n=$((target - have)); [ "$n" -le 64 ] || n=64
    rpc create_address "{\"account_index\":0,\"count\":$n}" >/dev/null
    have=$((have + n))
  done
  rpc store '{}' >/dev/null
  wallet_rpc_stop
  mark subaddresses
fi
ok "Moved v2's wallet past the subaddresses v1 handed out"
quietly up.log compose up -d
mark started

step 6 "Checking v2"
health=""
for _ in $(seq 45); do
  health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' nerostr 2>/dev/null || true)
  [ "$health" = healthy ] && break
  sleep 2
done
status=""
if [ "$health" = healthy ]; then
  info "Waiting for the first payment check..."
  port=$(compose port nerostr 8080 2>/dev/null) || true
  url=http://${port:-127.0.0.1:8080}/healthz
  for _ in $(seq 30); do
    status=$(curl -fsS -m 5 "$url" 2>/dev/null) || true
    grep -q '"healthy": *true' <<<"$status" && break
    sleep 4
  done
fi
daemon=$(sed -n "s/^MONERO_DAEMON_ADDRESS=//p" .env | tr -d "'")
if ! grep -q '"healthy": *true' <<<"$status"; then
  problem=$(grep -o '"error": *"[^"]*"' <<<"$status" | cut -d'"' -f4) || true
  warn "v2 is not healthy yet (${problem:-container status: ${health:-missing}}). Recent logs:"
  docker logs --tail 15 nerostr 2>&1 | sed 's/^/    /'
  info "Fix the cause and run the script again to recheck."
elif node_reachable "$daemon"; then
  ok "v2 is running, and its wallet reaches the Monero node"
  mark switched
else
  warn "v2 is running, but its wallet gets no answer from the Monero node $daemon."
  info "Set MONERO_DAEMON_ADDRESS in .env, run 'docker compose up -d', then run the script again."
fi

cat <<EOF

${B}Next${N}
  1. Point your TLS reverse proxy at 127.0.0.1:8080, for the paywall and the relay alike
     (strfry is gone). Examples for Caddy, nginx and caddy-docker-proxy:
     https://github.com/pluja/nerostr/tree/main/deploy
  2. Check https://$HOST/healthz
EOF
if [ "$(cat "$WORK/wallet-name")" != "$W_NAME" ]; then
  cat <<EOF
  3. Your v1 wallet, with its spend key, is still in $V1_WALLET.
     Once you won't roll back and your seed is backed up offline, delete it from this server.
EOF
fi
printf '\n%sRoll back%s\n  cd %q && docker compose down && cd %q && docker compose up -d\n' "$B" "$N" "$V2" "$V1"
printf '\n%s%s holds the event exports; delete it once you%sre happy.%s\n' "$DIM" "$WORK" "'" "$N"
