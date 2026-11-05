# monero-wallet-rpc from the official release binaries.
# Hashes come from https://www.getmonero.org/downloads/hashes.txt, verified
# against binaryFate's GPG key (81AC 591F E9C4 B65C 5806 AFC3 F0AF 4D46 2A0B DF92).
# To upgrade: bump MONERO_VERSION and both hashes after verifying the new hashes.txt.

FROM debian:trixie-slim AS download
ARG MONERO_VERSION=v0.18.5.4
ARG SHA256_AMD64=ba2928962c101b609434bf8c1b43ad59ebe96c5525977284ca599d8bf0f02d10
ARG SHA256_ARM64=d20d71e6522b41eb8e549c3425f7f4622c9c9a448fc7147525393fd0a6b59703
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl bzip2 \
 && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) arch=x64;   sum=$SHA256_AMD64 ;; \
      arm64) arch=armv8; sum=$SHA256_ARM64 ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o monero.tar.bz2 "https://downloads.getmonero.org/cli/monero-linux-${arch}-${MONERO_VERSION}.tar.bz2"; \
    echo "${sum}  monero.tar.bz2" | sha256sum -c -; \
    mkdir /monero; \
    tar -xjf monero.tar.bz2 -C /monero --strip-components=1; \
    /monero/monero-wallet-rpc --version

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 1000 monero \
 && useradd --uid 1000 --gid 1000 --home-dir /wallet --no-create-home --shell /usr/sbin/nologin monero \
 && mkdir /wallet && chown monero:monero /wallet
COPY --from=download /monero/monero-wallet-rpc /usr/local/bin/monero-wallet-rpc
USER monero
WORKDIR /wallet
EXPOSE 18083
ENTRYPOINT ["monero-wallet-rpc", "--non-interactive", "--rpc-bind-ip=0.0.0.0", "--confirm-external-bind", "--rpc-bind-port=18083"]
