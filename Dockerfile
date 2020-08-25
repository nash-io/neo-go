# Builder image
FROM golang:1-alpine as builder

RUN set -x \
    && apk add --no-cache git make screen expect \
    && mkdir -p /tmp

COPY . /neo-go

WORKDIR /neo-go

ARG REPO=repository
ARG VERSION=dev

RUN make

# Executable image
FROM alpine

RUN apk add --no-cache screen expect


ARG   VERSION
LABEL version=$VERSION

WORKDIR /

COPY --from=builder /neo-go/.docker/wallets/wallet1.json /wallet1.json
COPY --from=builder /neo-go/.docker/wallets/wallet2.json /wallet2.json
COPY --from=builder /neo-go/.docker/wallets/wallet3.json /wallet3.json
COPY --from=builder /neo-go/.docker/wallets/wallet4.json /wallet4.json
COPY --from=builder /neo-go/config /config
COPY --from=builder /neo-go/.docker/chain.acc /chain.acc
COPY --from=builder /neo-go/.docker/privnet-entrypoint.sh /usr/bin/privnet-entrypoint.sh
COPY --from=builder /neo-go/.docker/multi-privnet-entrypoint.sh /usr/bin/multi-privnet-entrypoint.sh
COPY --from=builder /neo-go/bin/neo-go /usr/bin/neo-go
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

EXPOSE 20333
EXPOSE 20334
EXPOSE 20335
EXPOSE 20336

EXPOSE 30333
EXPOSE 30334
EXPOSE 30335
EXPOSE 30336

ENTRYPOINT ["/bin/sh","/usr/bin/multi-privnet-entrypoint.sh"]
