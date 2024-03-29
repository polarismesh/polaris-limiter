#!/bin/bash

LIMITER_MY_ID=${MY_ID}

if [ "${LIMITER_MY_ID}" = "" ]; then
    HOST=`hostname -s`
    echo "CURRENT POD HOSTNAME : ${HOST}"
    if [[ $HOST =~ (.*)-([0-9]+)$ ]]; then
        NAME=${BASH_REMATCH[1]}
        ORD=${BASH_REMATCH[2]}
    else
        echo "Fialed to parse name and ordinal of Pod"
        exit 1
    fi
    LIMITER_MY_ID=$((ORD+1))
    echo "CURRENT POD MY_ID : ${LIMITER_MY_ID}"
fi

# 导出环境变量
export MY_ID="${LIMITER_MY_ID}"

# 格式化 /root/polaris-limiter.yaml 文件
envsubst </root/polaris-limiter.yaml.example >/root/polaris-limiter.yaml

# 运行 polaris-limiter
./polaris-limiter start
