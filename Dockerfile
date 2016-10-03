FROM debian:jessie
MAINTAINER P & P Capital Inc

ARG DEBIAN_FRONTEND=noninteractive

RUN apt-key adv --keyserver keys.gnupg.net --recv 886DDD89 && \
    echo 'deb http://deb.torproject.org/torproject.org jessie main' > /etc/apt/sources.list.d/tor.list

RUN apt-get update && \
    apt-get install -y haproxy tor privoxy && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ADD torotator /usr/local/bin/torotator

ENTRYPOINT ["/usr/local/bin/torotator"]
