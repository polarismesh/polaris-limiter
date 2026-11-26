FROM alpine:3.24.4

ENV BUILD_DEPS="gettext"  \
    RUNTIME_DEPS="libintl"

RUN sed -i \
    -e 's!http://dl-cdn.alpinelinux.org/!https://mirrors.tencent.com/!g' \
    -e 's!https://dl-cdn.alpinelinux.org/!https://mirrors.tencent.com/!g' \
    /etc/apk/repositories

RUN set -eux && \
    apk upgrade --no-cache && \
    apk add --no-cache tcpdump tzdata busybox-extras curl bash $RUNTIME_DEPS && \
    apk add --no-cache --virtual build_deps $BUILD_DEPS && \
    cp /usr/bin/envsubst /usr/local/bin/envsubst && \
    apk del build_deps && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    date

ARG TARGETARCH
COPY polaris-limiter-${TARGETARCH} /root/polaris-limiter
COPY ./deploy/kubernetes/start-limiter.sh /root/start-limiter.sh

WORKDIR /root

CMD ["bash", "/root/start-limiter.sh"]
