# 测试环境部署情况
1个consumer部署在eee1节点
2个provider分别部署在eee2和eee3节点（每节点各起 service-1 / service-2 两个实例）

# 环境
polaris-server已经云端部署,地址为172.16.0.5,
polaris-limiter服务已经云端部署,**2个节点**(polaris-limiter-0-0 / polaris-limiter-0-1),grpc端口为8101,http端口为8100,注册到了命名空间为Polaris,服务名为limiter的服务下

# 多服务 × 多 limiter 拓扑
polaris-go SDK 的 `GetMessageSender` 用 **Maglev 一致性哈希**选 limiter 实例,哈希 key = `<被限流服务名>#<命名空间>#<labels>`(见 `polaris-go/pkg/flow/quota/window.go:buildQuotaHashValue`)。故**同一被限流服务 → 相同 hashValue → 固定打到同一个 limiter 节点**。

若 2 个 provider 注册同一服务,只有一个 limiter 节点会被命中。为此采用**多服务拓扑**:让 provider 注册 2 个不同服务(`GlobalRatelimitEchoServer-1` / `-2`),每服务跨 eee2/eee3 各 1 实例(共 2 实例,保留跨节点共享配额语义),两服务 hashValue 不同 → 期望分散到 2 个 limiter 节点。

| 组件 | 部署 |
| --- | --- |
| limiter | 2 节点(云端,`Polaris/limiter`),不动 |
| service-1 `GlobalRatelimitEchoServer-1` | eee2(:18200) + eee3(:18200),共 2 实例 → 期望落 limiter A |
| service-2 `GlobalRatelimitEchoServer-2` | eee2(:18202) + eee3(:18202),共 2 实例 → 期望落 limiter B |
| consumer | eee1(:18201),串行验证 service-1 / service-2 |

> 限制:Maglev 一致性哈希**不保证**两服务一定落到不同 limiter 节点(2 节点环上两 hashValue 可能同侧)。脚本无法跨节点访问 limiter `/metrics`,故只打印 limiter 实例地址 + 人工核对提示,不计入 PASS/FAIL;若两服务 counter 都在同一 limiter Pod 日志,换服务名后缀(如 `-3`/`-4`)重试。

# 脚本使用
参考 `/Users/evelynwei/go/src/polarismesh/polaris-limiter/test/integration/test.sh`(本地单机 E2E),适配为云端多服务拓扑。验证范围:多服务分布式限流 + 每服务跨节点共享配额 + 驱动 2 个 limiter 节点(不验证 limiter /metrics,云端 limiter 与 consumer 跨节点网络不通)。

各目录下 `x86-bin` 为预编译 Linux x86_64 二进制(provider-qps / consumer),`polaris.yaml` 已固化 172.16.0.5 + limiterService=limiter,无需改动。

脚本行为总览:

| 脚本 | 创建/刷新的限流规则 | 启动的服务 |
| --- | --- | --- |
| `provider.sh` | 无(不创建任何规则,规则由 consumer.sh 负责下发) | 每次启动 1 个 provider 实例:`x86-bin --service <svc> --port <port>`,注册到 polaris 的 `default/<svc>`;pidfile/logfile 按服务名区分(`provider-<svc>.pid/.log`),同节点可并存 service-1/service-2 |
| `consumer.sh` | 每服务 5 条 GLOBAL 规则:1 条 `/echo`(`ratelimit-cloud-global-rule-<N>`)+ 4 条 agg(`/agg1-4` + `x-route=a/b/c/d`,`...-<N>-agg1..4`),默认 2 服务共 10 条 | 串行启动 consumer 实例:`x86-bin --service <svc> --port 18201`,每服务验证完即 kill 复用 18201 |

## 1. provider 节点(eee2 / eee3)

`provider.sh` **不创建任何限流规则**,仅启动 provider 服务实例并向 polaris 注册。每台机器执行 2 次(各起 1 个服务实例),构成每服务跨 eee2/eee3 各 1 实例的拓扑。

```shell
cd provider-qps
./provider.sh --polaris-server 172.16.0.10
# service-1(默认),eee2/eee3 各执行一次 → service-1 跨节点 2 实例, service=GlobalRatelimitEchoServer-1 port=18200
./provider.sh start --polaris-server 172.16.0.10
# service-2,eee2/eee3 各执行一次 → service-2 跨节点 2 实例
./provider.sh start --polaris-server 172.16.0.10 --service GlobalRatelimitEchoServer-2 --port 18202
# 查看 PID + polaris 注册情况(默认查 service-1)
./provider.sh status --polaris-server 172.16.0.10
# 查 service-2 
./provider.sh status --polaris-server 172.16.0.10 --service GlobalRatelimitEchoServer-2
# 停 service-2(须带 --service 定位 pidfile)
./provider.sh stop --service GlobalRatelimitEchoServer-2 --port 18202   

tail -f provider-GlobalRatelimitEchoServer-1.log -n 10
```
- pidfile/logfile 按服务名区分:`provider-<service>.pid` / `provider-<service>.log`,同节点可并存 service-1 / service-2 两个实例。
- 自检:启动后轮询本地 `/echo` 可达 + 实例在 polaris 标记为 healthy(最长 40s)。
- 出口 IP 由 SDK dial polaris-server 自动探测,无需手动指定 host。
- 鉴权:`POLARIS_TOKEN=xxx ./provider.sh start`(注入 polaris.yaml 的 ${POLARIS_TOKEN} 占位)。
- DEBUG:`./provider.sh start --debug`。
- **stop/status 须带 `--service`(及对应 `--port`)** 定位到对应服务的 pidfile,否则只作用于默认 service-1。

## 2. consumer 节点(eee1)

`consumer.sh` 串行验证 `SERVICES` 列表(默认 `GlobalRatelimitEchoServer-1` / `-2`)中的每个服务,每服务:**创建/刷新该服务的 GLOBAL 限流规则** + **启动 consumer 服务**跑 Case A/B/C。

- **限流规则**(每服务 5 条,作用于 `default/<svc>`):
  - 1 条 `/echo` 规则 `ratelimit-cloud-global-rule-<N>`:`method=EXACT /echo`,Case A/B 行为验证用。
  - 4 条 agg 规则 `ratelimit-cloud-global-rule-<N>-agg1..4`:`method=EXACT /agg1..4` + `arguments=[{HEADER x-route=a/b/c/d}]`,Case C 累加验证用。不同 `(method, argument)` 产生不同 hashValue,分散到 2 limiter Pod。
  - 统一参数:`resource=QPS`,`type=GLOBAL`,`action=REJECT`,`failover=FAILOVER_LOCAL`,`priority=0`,`disable=false`,`amounts=[{maxAmount=4, validDuration=1s}]`。
  - 幂等:规则已存在则 `PUT` 刷新参数(含 type 校正),不存在则 `POST` 创建;脚本退出时**不删除**规则,下次复用。
  - 关键:agg 规则 `arguments[].key` 必须小写 `x-route`(provider 用 lowercase 提取 header);请求必须带 `X-Route: <v>` 才匹配,否则全 200 不限流。
- **consumer 服务**:每服务启动一个 `x86-bin --service <svc> --port 18201` nohup 进程,SDK 拉取该服务规则,经 gRPC 与云端 limiter 同步配额;该服务验证完即 kill,复用 18201 起下一服务。

需先在 eee2/eee3 完成上面步骤(service-1 / service-2 各 2 实例 healthy)。然后:
```shell
cd consumer
./consumer.sh --polaris-server 172.16.0.10
# 默认串行验证 service-1/service-2,每服务 180s(Case A/B/C 各 60s),合计 ~12 分钟
```

执行流程:
1. 探测 polaris-server(172.16.0.5:8090)可达。
2. 查询 `Polaris/limiter` 实例列表,打印 2 个 limiter 节点地址 + 人工核对提示(见下)。
3. 对每个服务串行执行 4~11:
4. 创建/刷新该服务 5 条 GLOBAL 规则(1 `/echo` + 4 agg)。
5. 轮询等待该服务 ≥2 个 provider 实例 healthy(eee2/eee3)。
6. 启动 consumer(:18201,`--service` 该服务)。
7. 用例 A:经 consumer `/echo` 持续请求,验证 200/429 触发限流;完成后 kill consumer 腾出 18201。
8. 用例 B:直打该服务 eee2/eee3 两个 provider `/echo` 持续请求,验证跨节点共享同一远端配额(同一服务 hashValue 固定同一 limiter)。
9. 用例 C:直打两 provider 混合 `/agg1-4` + `X-Route: a/b/c/d` 持续请求,4 个 (path,route) 组合产生 4 hashValue 经 Maglev 分散到 2 limiter Pod,验证多 limiter 节点累加(见下)。
10. 反推该服务 monitor 预期上报值(A/B 桶):Case A/B 实测批次按自然分钟窗口聚合。
11. Case C 累加核对提示(`print_accumulate_hint`):打印两 Pod 地址 + Case C 总量 + 人工核对方式。
12. 汇总结论(PASS/FAIL)。

只验证分布式限流行为,不验证 limiter 的 /metrics。默认每服务持续请求 180s(Case A/B/C 各 60s),两服务串行合计 ~12 分钟,全量输出同时落盘到 `consumer/.logs/consumer-test-*.log`(去 ANSI 纯文本,退出时完整 flush 不丢尾部)。

### limiter 双节点命中人工核对
脚本会打印 `Polaris/limiter` 的 2 个 healthy 实例地址。验证后需人工核对两服务是否分散到不同 limiter Pod:
```shell
# 进两个 limiter Pod(命名空间 ins-87d1724e)
kubectl exec -it polaris-limiter-0-0 -c polaris-limiter -n ins-87d1724e -- /bin/bash
kubectl exec -it polaris-limiter-0-1 -c polaris-limiter -n ins-87d1724e -- /bin/bash
# 各 Pod 内 grep 对应服务的 counter init
grep 'GlobalRatelimitEchoServer-1' /root/log/polaris-limiter.log
grep 'GlobalRatelimitEchoServer-2' /root/log/polaris-limiter.log
```
期望:两服务的 counter init 分别出现在不同 Pod。若都集中在一个 Pod,说明两服务哈希到同一 limiter → 换服务名后缀(如 `-3`/`-4`)重试。此项不计入 PASS/FAIL。

### Case C 多 limiter 节点累加验证
Case C 验证**监控接收平台对同维度多 limiter 节点数据的累加**(当前 service-1→PodA、service-2→PodB 是不同 service 不同维度,接收平台不跨 service 累加,故需 Case C)。

机制:同一 service 创建 4 条 agg 规则(`/agg1-4` + `x-route=a/b/c/d`),4 个 `(method, argument)` 组合 → 4 个不同 hashValue → Maglev 分散到 2 limiter Pod。每个 Pod 处理部分组合的流量,产生该 service 的 counter。monitor(sidecar)每分钟 :15 拉取 /metrics,**不累加**,按 `polarisinstanceid+ratelimitcalleenamespace+ratelimitcalleeservice` 维度上报(抹平 method/labels);两 Pod 同 `polarisinstanceid=ins-87d1724e`,**接收平台把同分钟同维度数据相加** = 该 service 总流量。

`print_accumulate_hint` 打印 Case C 预期总量 + 人工核对方式:
```shell
# 进两 Pod 各取该 service 的 ratelimit_rq_total(prometheus label service=<svc>)
kubectl exec -it polaris-limiter-0-0 -c polaris-limiter -n ins-87d1724e -- \
    curl -s localhost:8100/metrics | grep 'ratelimit_rq_total{' | grep 'service="GlobalRatelimitEchoServer-1"'
kubectl exec -it polaris-limiter-0-1 -c polaris-limiter -n ins-87d1724e -- \
    curl -s localhost:8100/metrics | grep 'ratelimit_rq_total{' | grep 'service="GlobalRatelimitEchoServer-1"'
# 两 Pod ratelimit_rq_total 之和应 ≈ 脚本 Case C 总量(口径差 2-5%)
# 接收平台 record: polaris_limiter_request_count:sum{ratelimitcalleeservice=...} (两 Pod 相加) ≈ Case C 总量
```
- Maglev 不保证 4 hashValue 一定分散到 2 Pod;若 counter 全落同一 Pod,换 path/x-route 组合(如 `/agg5-8` + `e/f/g/h`)重试。
- Case C 行为 PASS 仅表示限流触发(argument 匹配成功);若 `limited=0 且 other=0`(全 200),是 argument 未匹配(curl 未带 `X-Route` 或规则 key 大小写不符)。**累加值正确与否不计入 PASS/FAIL**,由人工核对。

### Step 9 预期上报值口径
脚本每个流量批次记录 `<epoch> <200数> <429数> <其他数>` 到该服务的临时桶文件,Step 9 用 awk 按自然分钟(epoch 向下取整到 60s)聚合,给出人工比对 monitor 的依据(每服务独立输出):

- **维度映射**:`200 → request_pass_count`,`429 → request_limit_count`,`pass+limit → request_count`;维度 label 为 `ratelimitcalleenamespace=<ns>` / `ratelimitcalleeservice=<svc>`(均小写,service-1/service-2 各一组)。
- **时序对齐**:limiter flushLoop 对齐 `:00`,flushOnce 取「上一分钟」`[M:00,(M+1):00)` 增量累加到 Counter;monitor cron `15 */1 * * * ?` 每分钟 `:15` 抓取 `/metrics` 上报 delta(=上一分钟流量);record 时间戳归到拉取后下一个整点 → **`[M:00,(M+1):00)` 窗口的流量在 record `(M+2):00` 出现**(实测 record `T:00` ≈ 脚本 `[T-2:00]`,两服务一致)。
- **归窗口径**:批次按「发起时刻」归窗(贴近 limiter 按到达时刻的口径);跨 `:00` 的个别请求可能归窗不同,**相邻两窗口之和更稳定,总量最可靠**。
- **口径差**:脚本 429 含 SDK 本地兜底 reject,limiter counter 只计到达 gRPC 的请求,故脚本值通常略高于 record(口径差约 2-5%),属正常。
- **首窗防脉冲**:首个出现该维度的窗口,monitor 按防脉冲逻辑上报 delta=0(首值被吞),从第二个窗口起才与上表一致。

> Step 9 仅供人工核对 monitor 上报是否合理,不计入 PASS/FAIL。

常用选项:
```shell
./consumer.sh --duration 60                                  # 每服务流量时长缩到 60s(A/B/C 各 20s,调试用)
./consumer.sh --services svcA,svcB                           # 自定义服务列表
./consumer.sh --service OnlyOneService                       # 只验证单个服务
./consumer.sh --keep                                         # 保留日志(consumer 进程仍按服务 stop 以串行复用 18201)
POLARIS_TOKEN=xxx ./consumer.sh --polaris-server 172.16.0.10 # polaris-server 开启鉴权时
```

## 3. 清理脚本
本地进程 + 产物清理,默认不触碰 polaris 侧(规则复用、实例靠 SIGTERM 自行 deregister)。

### provider 节点(eee2 / eee3)
```shell
cd provider-qps
./clean-p.sh                # 默认: 展示后确认再清理进程 + provider-*.pid/provider-*.log/polaris/
./clean-p.sh -f             # 强制直接清理
./clean-p.sh --dry-run      # 仅展示,不执行
./clean-p.sh --instances    # 额外注销本节点 polaris 残留实例(默认遍历 service-1/service-2,仅 host=本机出口IP)
```
- 进程先枚举 `provider-*.pid`(兼容旧 `provider.pid`),再用 ps 兜底匹配本目录 `x86-bin`(pidfile 被删 / 进程被 -9 时兜底)。
- SIGTERM → 1s → SIGKILL;`--instances` 用于进程异常退出后 polaris 上残留实例未下线的场景(遍历服务列表逐个注销)。

### consumer 节点(eee1)
```shell
cd consumer
./clean-c.sh                # 默认: 展示后确认再清理进程 + .logs/consumer-*.log/polaris/
./clean-c.sh -f             # 强制直接清理
./clean-c.sh --dry-run      # 仅展示,不执行
./clean-c.sh --rule         # 额外删除 polaris 限流规则(默认遍历 service-1/service-2,每服务删 /echo+agg1-4 共 5 条,共 10 条)
```
- consumer.sh 用 nohup 启动且无 pidfile(串行复用 18201,每服务验证完即 kill),clean-c.sh 用 ps 匹配 `x86-bin`。
- `--rule` 彻底重置时用:默认遍历两服务,每服务删 `/echo` + agg1-4 共 5 条(先查 id 再 DELETE,带 body 兜底);`--rule-name` 指定时只删一条;失败会提示去控制台手动删。

## 注意事项
- **多 limiter 命中**:Maglev 一致性哈希不保证两服务落不同 limiter,需按上文「limiter 双节点命中人工核对」核对两 Pod 日志,必要时换服务名后缀重试。
- **多 limiter 累加(Case C)**:同 service 多 `(method,argument)` 规则产生多 hashValue 分散 2 Pod,验证接收平台按 `instanceid+ns+service` 相加两 Pod。Maglev 不保证分散,需人工核对两 Pod /metrics,全落一 Pod 则换 path/x-route 组合重试;累加值不计 PASS/FAIL。
- 网络:consumer(eee1) 需能经 VPC 访问 provider(eee2/eee3) 注册的 IP:18200/18202;若不通,直打用例会 other>0。
- 鉴权:polaris-server 若开启鉴权,两脚本都需 `POLARIS_TOKEN`;consumer 的 polaris.yaml 无 token 字段,如开启鉴权需自行补 `serverConnector.token`。
- 日志:consumer.sh 全量输出同时落 `consumer/.logs/consumer-test-*.log`,provider 守护进程日志按服务落在 `provider/provider-<service>.log`。
