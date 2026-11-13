# The server side of sharetoken in one image: portal, coordinator, relay and
# mailserver. docker-compose.yml runs one container per program from it.
#
#   docker build -t sharetoken .
#   docker build -t sharetoken --build-arg ST_VERSION=1.0.0 --build-arg ST_COMMIT=$(git rev-parse --short=7 HEAD) .
ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY VERSION ./
COPY cmd ./cmd
COPY internal ./internal
# Without ST_VERSION the programs report <VERSION>-dev.docker: built from a tree, not from a release tag.
ARG ST_VERSION=
ARG ST_COMMIT=docker
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    V=${ST_VERSION:-$(tr -d ' \n' < VERSION)-dev.docker}; P=sharetoken/internal/version; \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X $P.Version=$V -X $P.Commit=$ST_COMMIT -X $P.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/ ./cmd/portal ./cmd/coordinator ./cmd/relay ./cmd/mailserver

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 sharetoken \
 && install -d -o sharetoken -g sharetoken -m 700 \
      /var/lib/sharetoken/portal /var/lib/sharetoken/coordinator /var/lib/sharetoken/relay /var/lib/sharetoken/mail
COPY --from=build /out/ /usr/local/bin/
COPY docker/entrypoint.sh /usr/local/bin/st-entrypoint
USER sharetoken
WORKDIR /var/lib/sharetoken
# portal: 8092 (HTTPS)   relay: 4001 (TCP and QUIC)   the coordinator listens nowhere
EXPOSE 8092 4001/tcp 4001/udp
ENTRYPOINT ["st-entrypoint"]
CMD ["portal"]
