#!/bin/sh
/usr/bin/neo-go db restore -p --config-name privnet.docker.one -i /chain.acc
/usr/bin/neo-go db restore -p --config-name privnet.docker.two -i /chain.acc
/usr/bin/neo-go db restore -p --config-name privnet.docker.three -i /chain.acc
/usr/bin/neo-go db restore -p --config-name privnet.docker.four -i /chain.acc

screen -dm /usr/bin/neo-go node --config-name privnet.docker.one
screen -dm /usr/bin/neo-go node --config-name privnet.docker.two
screen -dm /usr/bin/neo-go node --config-name privnet.docker.three
screen -dm /usr/bin/neo-go node --config-name privnet.docker.four

echo "started neo-go private net"

sleep infinity