#!/bin/sh

screen -dmS node1 expect /usr/bin/privnet-entrypoint.sh node --priv1
screen -dmS node2 expect /usr/bin/privnet-entrypoint.sh node --priv2
screen -dmS node3 expect /usr/bin/privnet-entrypoint.sh node --priv3
screen -dmS node4 expect /usr/bin/privnet-entrypoint.sh node --priv4
sleep infinity