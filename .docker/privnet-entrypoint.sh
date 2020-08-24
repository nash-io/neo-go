#!/bin/sh

BIN=/usr/bin/neo-go

if [ -z "$ACC" ]; then
  ACC=/chain.acc
fi

case $@ in
  "node"*)
  echo "=> Try to restore blocks before running node"
  if test -f $ACC; then
    ${BIN} db restore -p --config-path /config -i /chain.acc
  fi
    ;;
esac

${BIN} "$@"
