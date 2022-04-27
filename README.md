# Polaris Limiter

Polaris Limiter是北极星的分布式限流服务端，主要用于全局token的缓存及分配，北极星SDK接入Polaris Limiter可进行配额的获取及上报刷新。

## 快速入门

### 构建

环境准备

- Go 1.12 及以上版本，本项目依赖 go mod 进行包管理

```
./build.sh
```

### 运行

- 填入北极星服务端地址

由于Polaris Limiter通过北极星注册中心来进行集群管理，Polaris Limiter启动后，会将自己注册为北极星服务。因此，需要在polaris-limiter.yaml配置文件中，填入北极星服务端的地址、以及所需注册的服务名和命名空间信息：

```
registry:
  # 是否启动自注册
  enable: true
  # 北极星服务端地址
  polaris-server-address: 127.0.0.1:8091
  # 注册的目标服务名
  name: polaris.limiter
  # 注册的目标命名空间
  namespace: Polaris
  # 是否开启健康检查
  health-check-enable: true
```

- 启动Polaris Limiter

(1) 启动命令
```
./tool/start.sh
```

(2) 查看进程是否启动成功
```
./tool/p.sh
```

(3) 可以通过查看北极星控制台对应的服务下的实例，可以查看限流服务端的注册结果。
