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
	mrand "math/rand/v2"
	"net"
	"strings"
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

// 初始化后只读，不参与并发写
var (
	polarisServerAddress string
	polarisToken         string
)

// registryCtx 保存首次 selfRegister 时的参数，供重注册复用。
// 通过 atomic.Pointer 整体替换，避免字段粒度的竞争。
type registryCtx struct {
	cfg              *Registry
	servers          []apiserver.APIServer
	apiServerConfigs []apiserver.Config
	serverAddress    string
}

// registrar 管理注册状态与重注册流程；所有字段用 atomic 原语保护。
type registrar struct {
	ctx       atomic.Pointer[registryCtx]
	instances atomic.Pointer[[]*polaris.Instance]

	notFoundCount   atomic.Int32 // 心跳连续 NOT_FOUND 次数
	reRegisterCount atomic.Int32 // 重注册重试计数（用于指数退避）
	reRegistering   atomic.Int32 // 0/1 标志，是否正在执行重注册
}

var reg = &registrar{}

// registerFn 是 doSelfRegister 的注入点，便于单测替换。
// 调用前 r.ctx 必须已由 selfRegister 写入。
var registerFn = func(r *registrar) error {
	return r.doSelfRegister()
}

// nextReqID 基于服务名 + 单调递增计数生成请求跟踪 ID。
func nextReqID(instance *polaris.Instance) string {
	return fmt.Sprintf("%s_%d", instance.GetService().GetValue(), atomic.AddUint64(&rid, 1))
}

// initPolarisClient 初始化 Polaris 客户端地址与 token（启动期调用，后续只读）。
func initPolarisClient(registryCfg *Registry) (err error) {
	polarisServerAddress = registryCfg.PolarisServerAddress
	polarisToken = registryCfg.Token
	if len(polarisServerAddress) == 0 {
		return fmt.Errorf("polaris server address is required")
	}
	return nil
}

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
				snapshot := reg.loadInstances()
				if len(snapshot) == 0 {
					continue
				}
				_ = doWithPolarisClient(func(client polaris.PolarisGRPCClient) error {
					for _, instance := range snapshot {
						instance := instance
						clientCtx, cancel := CreateHeaderContextWithReqId(timeout, nextReqID(instance))
						resp, err := client.Heartbeat(clientCtx, instance)
						cancel()
						if err := reg.handleHeartbeatResp(ctx, instance, resp, err); err != nil {
							return err
						}
					}
					return nil
				})
			}
		}
	}()
}

// loadInstances 返回已注册实例的快照；nil 时返回空切片。
func (r *registrar) loadInstances() []*polaris.Instance {
	p := r.instances.Load()
	if p == nil {
		return nil
	}
	return *p
}

// handleHeartbeatResp 处理单次心跳响应；返回 error 表示心跳失败，上层终止本轮。
// 连续 NOT_FOUND 超阈值时触发异步重注册。
func (r *registrar) handleHeartbeatResp(ctx context.Context, instance *polaris.Instance,
	resp *polaris.Response, err error) error {
	if err != nil {
		log.Errorf("[Bootstrap] fail to send heartbeat, err is %v", err)
		return err
	}
	if resp.GetCode().GetValue() == notFoundResourceCode {
		cnt := r.notFoundCount.Add(1)
		log.Errorf("[Bootstrap] heartbeat response not found for instance %s:%d, notFoundCount: %d",
			instance.GetHost().GetValue(), instance.GetPort().GetValue(), cnt)
		if cnt > int32(maxHeartbeatFailCount) {
			r.triggerAsyncReRegister(ctx)
		}
		return fmt.Errorf("instance not found")
	}
	r.notFoundCount.Store(0)
	return nil
}

// triggerAsyncReRegister 触发异步重注册，使用 CAS 防止并发。
// asyncReRegister 必须由本函数作为唯一入口调起，否则其 defer 中的
// reRegistering.Store(0) 会与未配对的 CAS 造成状态错乱。
func (r *registrar) triggerAsyncReRegister(ctx context.Context) {
	if !r.reRegistering.CompareAndSwap(0, 1) {
		log.Infof("[Bootstrap] re-register already in progress, skip")
		return
	}
	go r.asyncReRegister(ctx)
}

// asyncReRegister 异步执行重注册，内部循环重试。
// 每轮按 reRegisterCount 计算退避延迟；成功后重置计数器。
//
// 注意：必须在 reRegistering == 1 的前提下调用（即由 triggerAsyncReRegister
// 唯一入口触发，或测试中显式 Store(1) 后直接调用）；defer 中会 Store(0)。
func (r *registrar) asyncReRegister(ctx context.Context) {
	defer r.reRegistering.Store(0)

	for {
		count := r.reRegisterCount.Load()
		if delay := calcReRegisterDelay(count); delay > 0 {
			log.Infof("[Bootstrap] re-register backoff, reRegisterCount: %d, delay: %v", count, delay)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Infof("[Bootstrap] re-register cancelled due to context done")
				return
			case <-timer.C:
			}
		}

		if r.ctx.Load() == nil {
			log.Errorf("[Bootstrap] re-register failed: saved registry config is nil")
			return
		}

		if err := registerFn(r); err != nil {
			r.reRegisterCount.Add(1)
			log.Errorf("[Bootstrap] re-register failed, err: %s", err.Error())
			if ctx.Err() != nil {
				return
			}
			continue
		}

		log.Infof("[Bootstrap] re-register success")
		r.notFoundCount.Store(0)
		r.reRegisterCount.Store(0)
		return
	}
}

// calcReRegisterDelay 计算重注册退避延迟。
// delay = min(serverTtl * 2^(count-1) + random(serverTtl), maxReRegisterDelay)；
// 首次触发（count=0）delay=0，立即重注册。
func calcReRegisterDelay(count int32) time.Duration {
	if count <= 0 {
		return 0
	}
	// 提前截断大指数：count=5 时 base 已经是 80s（> 60s 上限），
	// 继续累加只会在 float64 → int64 转换时溢出。
	const maxExp = 10
	if count > maxExp {
		return maxReRegisterDelay
	}
	base := float64(serverTtl) * math.Pow(2, float64(count-1))
	jitter := float64(mrand.Int64N(int64(serverTtl)))
	delay := time.Duration(base + jitter)
	if delay < 0 || delay > maxReRegisterDelay {
		return maxReRegisterDelay
	}
	return delay
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

// buildLocation 构造实例地域信息，注册到 Instance.Location，供 SDK 做跨地域就近路由。
// 地域信息须放在 Instance.Location 而非 metadata：就近路由只读 location 字段，
// 服务端 metadata 与 location 是两套独立数据。
//
// 三级均未配置（trim 后为空）时返回 nil，Polaris 服务端会按实例 IP 走 CMDB 推导地域；
// 配了任意一级时只下发非空层级，未配置的层级不构造 StringValue，保持注册报文干净。
func buildLocation(cfg *Registry) *polaris.Location {
	// 去除首尾空白，避免 yaml 引号值带空格导致地域匹配静默失效
	region := strings.TrimSpace(cfg.Region)
	zone := strings.TrimSpace(cfg.Zone)
	campus := strings.TrimSpace(cfg.Campus)
	if region == "" && zone == "" && campus == "" {
		return nil
	}
	loc := &polaris.Location{}
	if region != "" {
		loc.Region = &wrappers.StringValue{Value: region}
	}
	if zone != "" {
		loc.Zone = &wrappers.StringValue{Value: zone}
	}
	if campus != "" {
		loc.Campus = &wrappers.StringValue{Value: campus}
	}
	return loc
}

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
	instance.Location = buildLocation(cfg)
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

func selfRegister(cfg *Registry, servers []apiserver.APIServer, apiServerConfigs []apiserver.Config, serverAddress string) error {
	reg.ctx.Store(&registryCtx{
		cfg:              cfg,
		servers:          servers,
		apiServerConfigs: apiServerConfigs,
		serverAddress:    serverAddress,
	})
	return registerFn(reg)
}

// doSelfRegister 执行真正的注册操作，成功后把 heartbeat instance 列表写回 registrar。
// 调用前必须已将 registryCtx 写入 r.ctx。
func (r *registrar) doSelfRegister() error {
	saved := r.ctx.Load()
	if saved == nil {
		return fmt.Errorf("registry context not initialized")
	}
	cfg, servers, apiServerConfigs, serverAddress :=
		saved.cfg, saved.servers, saved.apiServerConfigs, saved.serverAddress

	serverCfgMap := make(map[string]apiserver.Config, len(apiServerConfigs))
	for _, sc := range apiServerConfigs {
		serverCfgMap[sc.Name] = sc
	}
	instances := make([]*polaris.Instance, 0, len(servers))
	for _, server := range servers {
		serverCfg := serverCfgMap[server.GetProtocol()]
		if !serverCfg.ShouldRegister() {
			log.Infof("[Bootstrap] api server(%s) register is disabled, skip registration", server.GetProtocol())
			continue
		}
		instance := buildRegisterRequest(cfg, server, serverCfg, serverAddress)
		instances = append(instances, instance)
	}
	heartbeatInstances := make([]*polaris.Instance, 0, len(instances))
	err := doWithPolarisClient(func(client polaris.PolarisGRPCClient) error {
		for _, instance := range instances {
			instance := instance
			clientCtx, cancel := CreateHeaderContextWithReqId(timeout, nextReqID(instance))
			resp, err := client.RegisterInstance(clientCtx, instance)
			cancel()
			if err != nil {
				log.Errorf("[Bootstrap] fail to register instance %s:%d, err: %s",
					instance.GetHost().GetValue(), instance.GetPort().GetValue(), err)
				return err
			}
			log.Infof("[Bootstrap] instance %s:%d registered, code %d",
				instance.GetHost().GetValue(), instance.GetPort().GetValue(), resp.GetCode().GetValue())
			heartbeatInstances = append(heartbeatInstances, &polaris.Instance{
				Id:           &wrappers.StringValue{Value: resp.GetInstance().GetId().GetValue()},
				Namespace:    instance.GetNamespace(),
				Service:      instance.GetService(),
				Host:         instance.GetHost(),
				Port:         instance.GetPort(),
				ServiceToken: instance.GetServiceToken(),
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.instances.Store(&heartbeatInstances)
	return nil
}

func selfDeregister() error {
	snapshot := reg.loadInstances()
	if len(snapshot) == 0 {
		return nil
	}
	return doWithPolarisClient(func(client polaris.PolarisGRPCClient) error {
		for _, instance := range snapshot {
			instance := instance
			clientCtx, cancel := CreateHeaderContextWithReqId(timeout, nextReqID(instance))
			resp, err := client.DeregisterInstance(clientCtx, instance)
			cancel()
			if err != nil {
				log.Errorf("[Bootstrap] fail to deregister instance err: %s", err.Error())
				continue
			}
			log.Infof("[Bootstrap] success to deregister instance %s:%d, code %d",
				instance.GetHost().GetValue(), instance.GetPort().GetValue(), resp.GetCode().GetValue())
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
