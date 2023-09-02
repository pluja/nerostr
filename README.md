# nerostr: a nostr monero-paid relay

Everyone is free to read, but only paid users can publish events.

**A Nostr expensive relay paid with Monero and written in Go!**

- Check it out here: https://xmr.usenostr.org
- Add the expensive relay to your list: `wss://xmr.usenostr.org`.

## Contents

- [Features](#features)
- [Why?](#why)
- [Self-hosting your own relay](#self-hosting-your-own-relay)
    - [Reverse proxies examples](#reverse-proxies-examples)
- [How it works](#brief-explanation-of-how-it-works)
- [Nerostr API](#api)
    - [Whitelist a pubkey](#whitelist-a-pubkey)
    - [Remove a pubkey from the whitelist](#remove-a-pubkey-from-the-whitelist)
- [Migrating from previous versions](#migrating-from-previous-versions)
- [Support this project](#-support-this-project)

## Features

- Very easy to self-host.
- Lightweight and fast.
    - Uses `strfry` nostr relay, which is a very fast and efficient relay.
    - Uses `badgerdb` as a database, which is a fast and efficient golang database.
    - Talks directly with the `monero-wallet-rpc` to check for payments and get subaddresses.
- Simple UI written using Golang templates and TailwindCSS.

## Why?

Nostr has no spam filters/control. With public relays, the global feed and public chat channels get filled with spam, making them unusable.

In order to avoid spam in your feed, you pay a small fee (~$1) to a relay. Your pubkey gets whitelisted in that relay, and then you are able to publish events there. Reading from these relays is always free for everyone! This allows getting much more curated and clean results in the global page.

Also, paid relays can build up communities of users interested in particular topics, allowing users to follow relays based on their interests.

TLDR; Pay-to-relay to avoid nostr spammers.

## Self-hosting your own relay

Selfhosting a Nerostr relay is very easy, you just need a small server and Docker.

1. Get the `docker-compose.yml` file from this repo.

```bash
wget https://raw.githubusercontent.com/pluja/nerostr/master/docker-compose.yml
```

2. Create a new Monero wallet, get the `wallet` and `wallet.keys` files and put them in `./nerostr_data/wallet/` folder.
    - You can use the `monero-wallet-cli` to create a new wallet, or use [Feather Wallet](https://moneroaddress.org/) for a GUI wallet.

3. Get the `.env` file from this repo and modify all the variables to your needs. Mainly you will have to edit the `MONERO_WALLET_FILENAME` and `MONERO_WALLET_PASSWORD` variables.

```bash
wget -o .env https://raw.githubusercontent.com/pluja/nerostr/master/example.env
```

4. (optional) Get the config file for your the `strfry` relay:

```bash
wget -o strfry.conf https://raw.githubusercontent.com/pluja/nerostr/master/strfry/strfry.conf
```

[!WARNING]
You can change the `strfry` config as you want, but you must make sure to have the `plugin = "/app/nerostr-auth.sh"` line in the `writePolicy` section. If you don't have this, the paywall won't do anything and all events will be accepted by the relay.

### Reverse proxies examples

The following configurations assume that the webserver containers are in the same docker network as the Nerostr `nerostr` and `strfry-relay` containers. If that is not the case, you can bind a local port for each of the `nerostr` and `strfry` containers, and then use `localhost:<PORT>` in the reverse proxy configuration.

#### Caddy

```
xmr.usenostr.org {
	@websockets {
		header Connection *Upgrade*
		header Upgrade	websocket
	}

	reverse_proxy @websockets strfry-nerostr-relay:8080
	reverse_proxy nerostr:8080
}
```

#### Nginx

```
server {
    listen 80;
    server_name xmr.usenostr.org;

    location / {
        proxy_pass http://nerostr:8080;
    }

    location / {
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_pass http://strfry-nerostr-relay:8080;
    }
}
```

## Brief explanation of how it works

Users go to the `paywall` frontend, enter their `pubkey` either in npub or hex format, and pay a small fee in Monero. Once paid, their pubkey gets added to the whitelist database.

The `strfry` relay uses the nerostr-auth plugin to check if the pubkey of the user is in the whitelist database. If it is, the event gets accepted and published. If it is not, the event gets rejected.

The payment monitor talks directly with the `monero-wallet-rpc` to check for payments.

## API

Nerostr has a simple API that allows you to manage the whitelist database.

### Whitelist a pubkey

To add a new user to the whitelist, you can use the API:

```bash
curl --request POST \
    --url <NEROSTR_INSTANCE>/api/user/<PUBKEY> \
    --header "X-API-KEY: <API_KEY>"
```

Replace the variables with your own values. If you set an API key in the `.env` file, use that one. If you didn't set it, you can get it from the application startup logs, check them with `docker compose logs nerostr | head`.

### Remove a pubkey from the whitelist

To remove a pubkey from the whitelist, you can use the API:

```bash
curl --request DELETE \
    --url <NEROSTR_INSTANCE>/api/user/<PUBKEY> \
    --header "X-API-KEY: <API_KEY>"
```

Replace the variables with your own values. If you set an API key in the `.env` file, use that one. If you didn't set it, you can get it from the application startup logs, check them with `docker compose logs nerostr | head`.

## Migrating from previous versions

If you are migrating from a previous version of Nerostr, or you want to whitelist a list of pubkeys, you will have to do the following:

1. Get all the whitelisted pubkeys from your previous Nerostr instance:

```bash
sudo sqlite3 nerostr.db "SELECT pub_key FROM users WHERE status='ok';" > keys.txt
```

2. Get the keys.txt file, put it in a folder and add the followint bash script to that folder:

```bash
#!/bin/bash

input_file=$1
nerostr_host=$2
api_key=$3

# Read the input file line by line
while IFS= read -r pubkey
do
  # Run the curl command for each key
  curl --request POST \
    --url $nerostr_host/api/user/$pubkey \
    --header "X-API-KEY: $api_key"
done < "$input_file"
```

3. Run the script with the following command:

```bash
./script.sh /path/to/keys.txt <YOUR.NEROSTR.HOST.COM> <API_KEY>
```

Where `<YOUR.NEROSTR.HOST.COM>` is the domain of your Nerostr instance (with http/https), and `<API_KEY>` is the API key you have set in the `.env` file, or gotten in the application startup logs (if you haven't set it in .env file).

This will add all the pubkeys to the whitelist database.

## 🧡 Support this project

[kycnot.me/about#support](https://kycnot.me/about#support)