FROM alpine:3.13.6

ENV BUILD_DEPS="gettext"  \
    RUNTIME_DEPS="libintl"

RUN sed -i 's!http://dl-cdn.alpinelinux.org/!https://mirrors.tencent.com/!g' /etc/apk/repositories

RUN set -eux && \
    apk add tcpdump && \
    apk add tzdata && \
    apk add busybox-extras && \
    apk add curl && \
    apk add bash && \
    apk add --update $RUNTIME_DEPS && \
    apk add --virtual build_deps $BUILD_DEPS &&  \
    cp /usr/bin/envsubst /usr/local/bin/envsubst && \
    apk del build_deps && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    date

COPY polaris-limiter /root/polaris-limiter
COPY ./deploy/kubernetes/start-limiter.sh /root/start-limiter.sh

WORKDIR /root

CMD ["bash", "/root/start-limiter.sh"]