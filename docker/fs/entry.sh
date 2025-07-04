#!/bin/sh

# avoid overwriting resolv.conf by other components
echo "resolvconf=NO" >> /etc/resolvconf.conf

if [ "$1" = "gateway" ]; then
    # gateway mode

    # configure waygate gateway
    echo "> Configuring waygate gateway"
    
    waygate gateway start --configure

    # update configuration permissions
    chmod 600 /etc/wireguard/wg0.conf
    chmod 600 /etc/caddy/Caddyfile

    mv /etc/service/iptables-server /etc/service-disabled/
    mv /etc/service/waygate-server /etc/service-disabled/
elif [ "$1" = "join" ]; then
    # server
    echo "> Joining waygate network as server"

    # disable some services
    mv /etc/service/caddy /etc/service-disabled/
    mv /etc/service/waygate-gateway /etc/service-disabled/
    mv /etc/service/iptables-gateway /etc/service-disabled/
    
    waygate join "$2"
elif [ "$1" = "server" ]; then
    if [ "$2" = "start" ]; then
        echo "> Starting waygate server"

        mv /etc/service/caddy /etc/service-disabled/
        mv /etc/service/waygate-gateway /etc/service-disabled/
        mv /etc/service/iptables-gateway /etc/service-disabled/
    elif [ "$2" = "down" ]; then
        echo "> Tearing down waygate server"

        waygate server down -f
        exit 0
    else
        echo "Invalid command. Use 'start' or 'down'."
        exit 1
    fi
else
    echo "Invalid command. Use 'gateway' or 'join <TOKEN>'."
    exit 1
fi

chmod +x /etc/service/*/run && chmod +x /etc/service/*/finish

/usr/sbin/runsvdir /etc/service
