# polaris-limiter IOHang 探针 — K8s 集群验证

在 **已部署 polaris-limiter 的 Kubernetes 集群** 上，对指定 Pod 验证 `/liveness`、`/readiness` 与探测文件，并可用容器内 FIFO 注入「写阻塞」模拟 IOHang（无需节点特权、不冻整盘）。

> 本地单机限流 E2E 仍用 [`../test.sh`](../test.sh)；本目录只覆盖 **IOHang 探针 / 摘流**，不打业务流量。

## 目录

```
test/integration/io-hang/
├── README.md           # 本文件
├── lib.sh              # kubectl / port-forward / HTTP JSON 公共函数
├── check-baseline.sh   # 正常态：/、/liveness、/readiness、探测文件
├── inject-hang.sh      # 将探测文件换成 FIFO，卡住 OpenFile/Sync
├── recover.sh          # 解开 FIFO，恢复普通文件
├── watch.sh            # 轮询 readiness / READY / restartCount
├── run.sh              # 串行：基线 → 注入 → 503/不重启 → 恢复 → 200，日志落盘并打包
├── pack-logs.sh        # 汇总打包所有运行日志，便于从测试机拷回
└── build-materials.sh  # 本地生成上云物料 dist/io-hang.zip
```

## 上云与日志回传

在本地生成物料，上传到能 `kubectl` 访问集群的云端测试机：

```bash
./build-materials.sh            # 生成 dist/io-hang/ + dist/io-hang.zip（无 zip 命令时为 .tar.gz）
./build-materials.sh --clean    # 清理 dist/
```

测试机上：

```bash
unzip io-hang.zip && cd io-hang
./run.sh --namespace ins-87d1724e --pod polaris-limiter-0-0
./pack-logs.sh                  # 生成 io-hang-logs-<时间戳>.zip，按提示 scp 回本地
```

`run.sh` 每次运行把日志写入 `.logs/<时间>-<pod>/`（可用 `--log-dir` / `LOG_DIR` 改目录），无论成功失败，结束时都会收集产物并打出同名 `.zip`，单次结果直接拷这个包即可：

| 文件 | 内容 |
|---|---|
| `run.log` | 脚本完整输出（已去颜色码），末尾为 PASS / FAIL |
| `1-baseline-*` / `2-hang-*` / `3-recovered-*` | 各阶段 `/readiness`、`/liveness` 响应（首行为 HTTP 码）与容器 ready/restart |
| `pod.yaml` / `pod-describe.txt` / `events.txt` | Pod 状态、探针失败事件 |
| `container-stdout.log`（`-previous.log`） | `kubectl logs`，含启动时打印的生效配置；发生重启时附上一个容器的输出 |
| `polaris-limiter.log` / `polaris-limiter-probe.log` / `pod-log-dir.txt` | 容器内业务日志尾部 5000 行、探测文件尾部、日志目录列表（探测文件仍为 FIFO 时跳过读取） |

## 前置

| 依赖 | 说明 |
|---|---|
| `kubectl` | 已配置目标集群，当前 context 能访问 limiter 所在 namespace |
| `curl` | 跑脚本的机器上（macOS / 跳板机均可） |
| `python3` | 解析 `/readiness` JSON（macOS 自带即可） |
| 镜像 | 已包含 IOHang 端点的 limiter 镜像（`/liveness`、`/readiness`） |

可选：`jq` 没有也行，脚本走 python3。

## 环境变量 / 参数

所有脚本共用（可用环境变量或 `--flag`，flag 优先）：

| 变量 | 默认 | 含义 |
|---|---|---|
| `NAMESPACE` / `--namespace` | 必填或自动探测 | Pod 所在 ns，如 `ins-87d1724e` |
| `POD` / `--pod` | 自动取第一个名字含 `polaris-limiter` 的 Pod | 目标 Pod |
| `CONTAINER` / `--container` | `polaris-limiter` | 容器名（不要用 monitor sidecar） |
| `HTTP_PORT` / `--http-port` | `8100` | limiter HTTP |
| `LOCAL_PORT` / `--local-port` | `18100` | 本机 port-forward 端口 |
| `PROBE_PATH` / `--probe-path` | `/root/log/polaris-limiter-probe.log` | 容器内探测文件 |
| `SERVICE` / `--service` | 空 | 若填写则检查 Endpoints 是否摘除该 Pod |
| `POLARIS_HTTP` / `--polaris-http` | 空 | 如 `http://172.16.0.5:8090`，则额外查北极星实例健康 |
| `POLARIS_TOKEN` / `--polaris-token` | 空 | 北极星开启鉴权时带 `X-Polaris-Token` |
| `POLARIS_NS` / `POLARIS_SVC` | `Polaris` / `polaris.limiter` | limiter 在北极星注册的命名空间与服务名 |

```bash
cd test/integration/io-hang
export NAMESPACE=ins-87d1724e
# 或不 export，每次带 --namespace
```

## 推荐流程（一条命令）

对 **一个** limiter Pod 做完整注入/恢复（约 1～2 分钟）：

```bash
./run.sh --namespace ins-87d1724e --pod polaris-limiter-0-0
```

期望：

1. 基线：`/` 为 `polaris limit server`；`/liveness` 200 UP；`/readiness` 200 且 `components.ioHang.status=UP`；探测文件含 `iohang-probe`。
2. 注入 FIFO 后 **3～8s** 内 `/readiness` 变 503，`ioHang` 为 DOWN（reason 含 `inflight`）。
3. 全程 `/liveness` 仍 200；`restartCount` **不变**（摘流而非重启）。503 之后 **5～10s** 内 limiter 容器 `ready=false`（脚本最多等 15s，未摘除只告警不失败）。
4. `recover` 后 **≤30s** `/readiness` 回到 200。恢复后第一次响应里可能带 `details.lastError: ... invalid argument`：被唤醒的那次探测对 FIFO 做 `fsync` 会返回 EINVAL，属预期，下一次探测（约 5s）即清除。

## 判定语义

| 情况 | `/readiness` | 心跳 |
|---|---|---|
| 探测写卡住超过 `timeout`（inflight） | 503 DOWN | 跳过 |
| 距上次探测完成超过 `staleness` | 503 DOWN | 跳过 |
| 写入立即返回**设备错误**（`EIO` / `ENXIO` / `ENODEV`） | 503 DOWN，reason 为 `device error: ...` | 跳过 |
| 写入**立即返回其他错误**（磁盘满、只读、路径不可写） | 200 UP，`details.lastError` 带原因 | 照常 |

NFS、virtio、部分云盘掉盘时常见快速返回 `EIO` 而非一直阻塞，属于单节点设备故障，换副本即可绕开，因此摘流；下一次探测成功即恢复 UP。磁盘满、只读、无权限这类环境错误往往全量副本同时出现，进程仍能正常服务限流，摘流反而会把服务整体摘空。首次出错和恢复时 limiter 会各打一条 `[Health]` 日志。

若 helm 探针仍打 `/`（未切到 `/liveness`+`/readiness`），K8s `READY` 可能一直是 Ready，**脚本仍以 HTTP `/readiness` 为准**，并打印告警。探针切完后可看到 limiter 容器 `ready=false`（双容器时常为 `1/2`）。

## 分步执行

```bash
# 1. 只检查正常态（不注入）
./check-baseline.sh --namespace ins-87d1724e --pod polaris-limiter-0-0

# 2. 注入 hang（探测文件 → FIFO）
./inject-hang.sh --namespace ins-87d1724e --pod polaris-limiter-0-0

# 3. 观察（Ctrl+C 结束）
./watch.sh --namespace ins-87d1724e --pod polaris-limiter-0-0 --interval 2

# 4. 恢复
./recover.sh --namespace ins-87d1724e --pod polaris-limiter-0-0
```

`run.sh --skip-inject` 等价于只跑 `check-baseline.sh`。

## 启动期 IOHang（手工）

验证「启动时磁盘已 hang 则不注册北极星，恢复后再注册」。日志目录挂在持久卷上（helm 部署为 hostPath，仓库 `deploy/kubernetes` 示例为 PVC `polaris-limiter-data`），FIFO 在容器重启后仍在，因此可以先注入再重启容器。若日志目录在容器可写层上，重启后 FIFO 丢失，该用例不适用。

limiter 是容器 PID 1（`start-limiter.sh` 用 `exec` 启动），`pkill` 发 SIGTERM 会走优雅退出（含反注册），随后 kubelet 拉起新容器：

```bash
./inject-hang.sh --namespace ins-87d1724e --pod polaris-limiter-0-0
# 只杀 limiter 进程让 kubelet 重启容器；restartCount +1 属预期
kubectl exec -n ins-87d1724e polaris-limiter-0-0 -c polaris-limiter -- pkill polaris-limiter
./watch.sh --namespace ins-87d1724e --pod polaris-limiter-0-0   # 新容器 /readiness 503，ready=false
```

期望：

1. 新容器 `/liveness` 200、`/readiness` 503，容器不再继续重启。
2. 北极星 `Polaris/polaris.limiter` 下**没有**该 Pod IP 的实例（或保持不健康），`POLARIS_HTTP` 已配置时 `run.sh` 用的 `query_polaris_instances` 也可手工调用核对。
3. `./recover.sh ...` 后约 10s 内实例出现且健康；limiter 日志含 `readiness recovered, start deferred polaris registration` 与 `re-register success`。

## 注入原理（FIFO，非 fsfreeze）

探测路径每次成功写后还会 `OpenFile(path)` + `Sync()`。将 `polaris-limiter-probe.log` **改名为 `.bak` 再 `mkfifo` 同名文件** 后：

- lumberjack 仍握着旧 inode，短写可能成功；
- 下一次 `OpenFile` 打开 FIFO 会 **一直阻塞**（直到有读者），inflight 超过 `timeout`（默认 3s）→ `/readiness` 503。

这不是块设备 hang，但足以验证：**handler 不阻塞、liveness 无 IO、readiness 转 503、进程不重启**。真盘 hang 仍建议在独立节点用 FUSE / `fsfreeze` / `dmsetup delay`（见方案文档）。

**不要在生产全量副本上同时注入。** 只打一个 Pod，避免限流服务被摘空。

## 与 helm 探针、北极星心跳

| 观察项 | 何时会变 | 说明 |
|---|---|---|
| `curl /readiness` | 注入后 3～8s | 本脚本主断言 |
| Pod READY | readiness 503 之后 5～10s | `periodSeconds=5 × failureThreshold=2` |
| 北极星实例不健康 | `affect-heartbeat=true` 时约 10～15s | 需 `POLARIS_HTTP` 或控制台人工看 `Polaris/polaris.limiter` |
| `/liveness` / restartCount | 全程不应失败 / 不应增加 | 核心验收：摘流不是重启 |

## 常见问题

**`/liveness` 或 `/readiness` 404**  
镜像未升级。发布顺序必须是镜像先于 helm 探针模板。

**注入后 readiness 仍 200**  
探测路径不对（工作目录不是 `/root`）。`--probe-path` 改成实际路径；或 `kubectl exec ... -- ls -l /root/log`。

**recover 后一直 503**  
FIFO 上仍有卡住的 `OpenFile`。`recover.sh` 会先 `cat` 一下 FIFO 唤醒写侧；若仍失败，再跑一次 `recover.sh`，或重启该容器（最后手段）。

**用 `chmod` / 删目录模拟故障，readiness 仍是 200**  
这类操作返回的是 `EACCES` / `ENOENT` 等环境错误，按设计不判 DOWN（见「判定语义」），看 `details.lastError` 即可。要验证摘流必须制造「阻塞」（本目录的 FIFO 注入）或设备错误（如在独立节点用 `dmsetup` 的 `error` target 让读写返回 `EIO`）。

**`port-forward` 失败**  
改 `--local-port`；确认本机端口未被占用。
