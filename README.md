# nerostr: a nostr monero-paid relay

Everyone is free to read, but only paid users can publish events.

**A Nostr expensive relay paid with Monero and written in Go!**

- Check it out here: https://xmr.usenostr.org
- Add the expensive relay to your list: `wss://xmr.usenostr.org`.

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

4. Get the config file for your the `strfry` relay:

```bash
wget -o strfry.conf https://raw.githubusercontent.com/pluja/nerostr/master/strfry/strfry.conf
```

::: warning
You can change the `strfry` config as you want, but you must make sure to have the `plugin = "/app/nerostr-auth.sh"` line in the `writePolicy` section. If you don't have this, the paywall won't do anything and all events will be accepted by the relay.
:::

## How does it work?

Users go to the `paywall` frontend, enter their `pubkey` either in npub or hex format, and pay a small fee in Monero. Once paid, their pubkey gets added to the whitelist database.

The `strfry` relay uses the nerostr-auth plugin to check if the pubkey of the user is in the whitelist database. If it is, the event gets accepted and published. If it is not, the event gets rejected.

The payment monitor talks directly with the `monero-wallet-rpc` to check for payments.

## 🧡 Support this project

[kycnot.me/about#support](https://kycnot.me/about#support)