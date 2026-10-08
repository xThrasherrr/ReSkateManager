# ReSkateManager with the panel embedded, running Linux ReSkate servers.
#
#   docker build -t reskate-manager --build-arg VERSION=$(git describe --tags --always) .
#
# Everything the manager keeps (data/, servers/, cache/) lives in /data.

FROM node:22-trixie-slim AS web
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@9 --activate
COPY web/package.json web/pnpm-lock.yaml web/.npmrc ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm run build

FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/
COPY --from=web /src/web/dist web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ReSkateManager ./cmd/ReSkateManager

# glibc, not Alpine: ReSkateServer and Valve's steamclient.so are glibc builds.
FROM debian:trixie-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates libssl3t64 libstdc++6 zlib1g libzstd1 \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd --uid 1000 --create-home --home-dir /home/reskate reskate \
	&& mkdir /data && chown reskate: /data
COPY --from=build /out/ReSkateManager /usr/local/bin/ReSkateManager

# RSM_ROOT is the manager's --root. Self-update is off: pull a newer image instead.
ENV RSM_ROOT=/data RSM_NO_SELF_UPDATE=1
USER reskate
VOLUME /data
# Panel, then the game/query port pairs the manager hands out from 27015 (five servers).
EXPOSE 40125/tcp 27015-27024/udp
ENTRYPOINT ["ReSkateManager", "--no-tray", "--no-browser"]
