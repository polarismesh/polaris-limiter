/*
 * Tencent is pleased to support the open source community by making polaris-limiter available.
 *
 * Copyright (C) 2021 Tencent. All rights reserved.
 *
 * Licensed under the BSD 3-Clause License (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * https://opensource.org/licenses/BSD-3-Clause
 *
 * Unless required by applicable law or agreed to in writing, software distributed
 * under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
 * CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 */

package bootstrap

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net"
	"sync/atomic"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/polarismesh/polaris-limiter/apiserver"
	polaris "github.com/polarismesh/polaris-limiter/pkg/api/polaris/v1"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/version"
)

const (
	// 心跳上报周期
	serverTtl = 5 * time.Second
	// 需要发往服务端的请求跟踪标识
	headerRequestID = "request-id"

	timeout = 1 * time.Second

	// 心跳返回 NOT_FOUND 连续失败阈值，超过后触发重注册
	maxHeartbeatFailCount = 2
	// 重注册退避上限
	maxReRegisterDelay = 60 * time.Second
	// Polaris NotFoundResource 错误码
	notFoundResourceCode uint32 = 400202
)

var rid uint64

var (
	registerInstances []*polaris.Instance

	polarisServerAddress string
	polarisToken         string

	// 重注册所需的上下文（首次注册时保存）
	savedRegistryCfg      *Registry
	savedAPIServers       []apiserver.APIServer
	savedAPIServerConfigs []apiserver.Config
	savedServerAddress    string

	// 重注册状态（使用 atomic 操作保证并发安全）
	notFoundCount   int32 // NOT_FOUND 连续失败次数
	reRegisterCount int32 // 重注册重试计数（用于指数退避）
	reRegistering   int32 // 0/1 标志，是否正在执行重注册
)

// 初始化客户端SDK
func initPolarisClient(registryCfg *Registry) (err error) {
	polarisServerAddress = registryCfg.PolarisServerAddress
	polarisToken = registryCfg.Token
	if len(polarisServerAddress) == 0 {
		return fmt.Errorf("polaris server address is required")
	}
	return nil
}

// 启动心跳上报
func startHeartbeat(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(serverTtl)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Infof("[Bootstrap] heartbeat routine stopped")
				return
			case <-ticker.C:
				if len(registerInstances) == 0 {
					continue
				}
				_ = doWithPolarisClient(func(client polaris.PolarisGRPCClient) error {
					for _, instance := range registerInstances {
						instance := instance
						heartbeat := func() error {
							reqId := fmt.Sprintf("%s_%d", instance.GetService().GetValue(), atomic.AddUint64(&rid, 1))
							clientCtx, cancel := CreateHeaderContextWithReqId(timeout, reqId)
							defer cancel()
							resp, err := client.Heartbeat(clientCtx, instance)
							if nil != err {
								log.Errorf("[Bootstrap] fail to send heartbeat, err is %v", err)
								return err
							}
							// 检查响应码，判断是否为 NOT_FOUND
							code := resp.GetCode().GetValue()
							if code == notFoundResourceCode {
								cnt := atomic.AddInt32(&notFoundCount, 1)
								log.Errorf("[Bootstrap] heartbeat response not found for instance %s:%d, notFoundCount: %d",
									instance.GetHost().GetValue(), instance.GetPort().GetValue(), cnt)
								if cnt > int32(maxHeartbeatFailCount) {
									triggerAsyncReRegister(ctx)
								}
								return fmt.Errorf("instance not found")
							}
							// 心跳成功，重置失败计数
							atomic.StoreInt32(&notFoundCount, 0)
							return nil
						}
						if err := heartbeat(); nil != err {
							return err
						}
					}
					return nil
				})
			}
		}
	}()
}

// triggerAsyncReRegister 触发异步重注册，使用 CAS 防止并发
func triggerAsyncReRegister(ctx context.Context) {
	if !atomic.CompareAndSwapInt32(&reRegistering, 0, 1) {
		log.Infof("[Bootstrap] re-register already in progress, skip")
		return
	}
	go asyncReRegister(ctx)
}

// asyncReRegister 异步执行重注册，带指数退避 + 随机抖动
func asyncReRegister(ctx context.Context) {
	defer atomic.StoreInt32(&reRegistering, 0)

	count := atomic.LoadInt32(&reRegisterCount)
	delay := calcReRegisterDelay(count)

	log.Infof("[Bootstrap] triggering async re-register, reRegisterCount: %d, delay: %v", count, delay)

	// 等待退避时间，同时监听 ctx 取消
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			log.Infof("[Bootstrap] re-register cancelled due to context done")
			return
		case <-timer.C:
		}
	}

	// 执行重注册
	if savedRegistryCfg == nil {
		log.Errorf("[Bootstrap] re-register failed: saved registry config is nil")
		return
	}

	if err := selfRegister(savedRegistryCfg, savedAPIServers, savedAPIServerConfigs, savedServerAddress); err != nil {
		atomic.AddInt32(&reRegisterCount, 1)
		log.Errorf("[Bootstrap] re-register failed, err: %s", err.Error())
		return
	}

	// 重注册成功，重置所有计数器
	log.Infof("[Bootstrap] re-register success")
	atomic.StoreInt32(&notFoundCount, 0)
	atomic.StoreInt32(&reRegisterCount, 0)
}

// calcReRegisterDelay 计算重注册退避延迟
// delay = min(serverTtl * 2^(count-1) + random(serverTtl), maxReRegisterDelay)
// 首次触发（count=0）delay=0，立即重注册
func calcReRegisterDelay(count int32) time.Duration {
	if count <= 0 {
		return 0
	}
	base := float64(serverTtl) * math.Pow(2, float64(count-1))
	jitter := float64(rand.Int63n(int64(serverTtl)))
	delay := time.Duration(base + jitter)
	return min(delay, maxReRegisterDelay)
}

func doWithPolarisClient(handle func(polaris.PolarisGRPCClient) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	conn, err := grpc.DialContext(ctx, polarisServerAddress, opts...)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := polaris.NewPolarisGRPCClient(conn)
	return handle(client)
}

// 创建服务注册请求
func buildRegisterRequest(cfg *Registry, server apiserver.APIServer, serverCfg apiserver.Config, serverAddress string) *polaris.Instance {
	instance := &polaris.Instance{}
	instance.Namespace = &wrappers.StringValue{Value: cfg.Namespace}
	instance.Service = &wrappers.StringValue{Value: cfg.Name}
	instance.Host = &wrappers.StringValue{Value: serverAddress}
	// 如果配置了自定义注册端口则使用自定义端口，否则使用 APIServer 实际监听端口
	if serverCfg.RegisterPort > 0 {
		instance.Port = &wrappers.UInt32Value{Value: serverCfg.RegisterPort}
	} else {
		instance.Port = &wrappers.UInt32Value{Value: server.GetPort()}
	}
	instance.ServiceToken = &wrappers.StringValue{Value: polarisToken}
	instance.Protocol = &wrappers.StringValue{Value: server.GetProtocol()}
	instance.Version = &wrappers.StringValue{Value: version.Version}
	instance.Metadata = map[string]string{"build-revision": version.GetRevision()}
	if cfg.HealthCheckEnable { // 开启健康检查
		instance.EnableHealthCheck = &wrappers.BoolValue{Value: true}
		instance.HealthCheck = &polaris.HealthCheck{
			Type: polaris.HealthCheck_HEARTBEAT,
			Heartbeat: &polaris.HeartbeatHealthCheck{
				Ttl: &wrappers.UInt32Value{Value: uint32(serverTtl / time.Second)}}}
	}
	return instance
}

// CreateHeaderContextWithReqId 创建传输grpc头的valueContext
func CreateHeaderContextWithReqId(timeout time.Duration, reqID string) (context.Context, context.CancelFunc) {
	md := metadata.New(map[string]string{headerRequestID: reqID})
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx = context.Background()
		cancel = nil
	}
	return metadata.NewOutgoingContext(ctx, md), cancel
}

// 注册限流Server
func selfRegister(cfg *Registry, servers []apiserver.APIServer, apiServerConfigs []apiserver.Config, serverAddress string) error {
	// 保存注册上下文，供重注册使用
	savedRegistryCfg = cfg
	savedAPIServers = servers
	savedAPIServerConfigs = apiServerConfigs
	savedServerAddress = serverAddress

	// 构建 server name -> apiserver.Config 的映射
	serverCfgMap := make(map[string]apiserver.Config, len(apiServerConfigs))
	for _, sc := range apiServerConfigs {
		serverCfgMap[sc.Name] = sc
	}
	// 开始对每个监听端口的服务进行注册
	var instances = make([]*polaris.Instance, 0, len(servers))
	for _, server := range servers {
		serverCfg := serverCfgMap[server.GetProtocol()]
		// 检查是否需要注册，不需要注册的 server 跳过
		if !serverCfg.ShouldRegister() {
			log.Infof("[Bootstrap] api server(%s) register is disabled, skip registration", server.GetProtocol())
			continue
		}
		instance := buildRegisterRequest(cfg, server, serverCfg, serverAddress)
		instances = append(instances, instance)
	}
	var heartbeatInstances = make([]*polaris.Instance, 0, len(servers))
	err := doWithPolarisClient(func(client polaris.PolarisGRPCClient) error {
		for _, instance := range instances {
			instance := instance
			register := func() error {
				reqId := fmt.Sprintf("%s_%d", instance.GetService().GetValue(), atomic.AddUint64(&rid, 1))
				clientCtx, cancel := CreateHeaderContextWithReqId(timeout, reqId)
				defer cancel()
				resp, err := client.RegisterInstance(clientCtx, instance)
				if nil != err {
					log.Errorf("[Bootstrap] fail to register instance %s:%d, err: %s", instance.GetHost().GetValue(), instance.GetPort().GetValue(), err)
					return err
				}
				log.Infof("[Bootstrap] instance %s:%d registered, code %d", instance.GetHost().GetValue(), instance.GetPort().GetValue(), resp.GetCode().GetValue())
				hbInstance := &polaris.Instance{
					Id:           &wrappers.StringValue{Value: resp.GetInstance().GetId().GetValue()},
					Namespace:    instance.GetNamespace(),
					Service:      instance.GetService(),
					Host:         instance.GetHost(),
					Port:         instance.GetPort(),
					ServiceToken: instance.GetServiceToken(),
				}
				heartbeatInstances = append(heartbeatInstances, hbInstance)
				return nil
			}
			if err := register(); nil != err {
				return err
			}
		}
		return nil
	})
	if nil != err {
		return err
	}
	registerInstances = heartbeatInstances
	return nil
}

// 反注册
func selfDeregister() error {
	if len(registerInstances) == 0 {
		return nil
	}
	return doWithPolarisClient(func(client polaris.PolarisGRPCClient) error {
		for _, instance := range registerInstances {
			instance := instance
			deregister := func() error {
				reqId := fmt.Sprintf("%s_%d", instance.GetService().GetValue(), atomic.AddUint64(&rid, 1))
				clientCtx, cancel := CreateHeaderContextWithReqId(timeout, reqId)
				defer cancel()
				resp, err := client.DeregisterInstance(clientCtx, instance)
				if err != nil {
					log.Errorf("[Bootstrap] fail to deregister instance err: %s", err.Error())
					return err
				}
				log.Infof("[Bootstrap] success to deregister instance %s:%d, code %d", instance.GetHost().GetValue(), instance.GetPort().GetValue(), resp.GetCode().GetValue())
				return nil
			}
			_ = deregister()
		}
		return nil
	})
}

// GetLocalHost 获取本地IP地址
func GetLocalHost(probeAddr string) (string, error) {
	if len(probeAddr) == 0 {
		return "127.0.0.1", nil
	}
	// 使用 DialTimeout 替代 Dial，防止网络阻塞
	conn, err := net.DialTimeout("tcp", probeAddr, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()

	localAddr := conn.LocalAddr()
	tcpAddr, ok := localAddr.(*net.TCPAddr)
	if !ok {
		return "", fmt.Errorf("get local address format is invalid")
	}

	return tcpAddr.IP.String(), nil
}
