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
# 使用 exec 让 polaris-limiter 替换当前 bash 进程成为 PID 1，
# 这样 K8s 删除 Pod 时下发的 SIGTERM 能直达 polaris-limiter，
# 触发 runMainLoop → stopServers → selfDeregister 完成优雅反注册；
# 否则 bash 作为 PID 1 不会转发信号给子进程，导致反注册失败,实例残留为异常状态。
exec ./polaris-limiter start
