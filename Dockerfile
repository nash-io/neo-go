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

COPY --from=builder /neo-go/config /config
COPY --from=builder /neo-go/.docker/chain.acc /chain.acc
COPY --from=builder /neo-go/.docker/privnet-entrypoint.sh /usr/bin/privnet-entrypoint.sh
COPY --from=builder /neo-go/.docker/multi-privnet-entrypoint.sh /usr/bin/multi-privnet-entrypoint.sh
COPY --from=builder /neo-go/bin/neo-go /usr/bin/neo-go
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["/usr/bin/multi-privnet-entrypoint.sh"]

CMD ["node", "--config-path", "/config", "--privnet"]
