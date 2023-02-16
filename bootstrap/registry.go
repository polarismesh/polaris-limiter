/**
 * Tencent is pleased to support the open source community by making Polaris available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company. All rights reserved.
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
)

var rid uint64

var (
	registerInstances []*polaris.Instance

	polarisServerAddress string
	polarisToken         string
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
							_, err := client.Heartbeat(clientCtx, instance)
							if nil != err {
								log.Errorf("[Bootstrap] fail to send heatBeat, err is %v", err)
							}
							return err
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
func buildRegisterRequest(cfg *Registry, server apiserver.APIServer, serverAddress string) *polaris.Instance {
	instance := &polaris.Instance{}
	instance.Namespace = &wrappers.StringValue{Value: cfg.Namespace}
	instance.Service = &wrappers.StringValue{Value: cfg.Name}
	instance.Host = &wrappers.StringValue{Value: serverAddress}
	instance.Port = &wrappers.UInt32Value{Value: server.GetPort()}
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
func selfRegister(cfg *Registry, servers []apiserver.APIServer, serverAddress string) error {
	// 开始对每个监听端口的服务进行注册
	var instances = make([]*polaris.Instance, 0, len(servers))
	for _, server := range servers {
		instance := buildRegisterRequest(cfg, server, serverAddress)
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
					log.Infof("[Bootstrap] fail to register instance %d:%s, err: %s", instance.GetHost().GetValue(), instance.GetPort().GetValue(), err)
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
			err := register()
			if nil != err {
				return nil
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
	conn, err := net.Dial("tcp", probeAddr)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()

	localAddr := conn.LocalAddr().String() // ip:port
	items := strings.Split(localAddr, ":")
	if len(items) != 2 {
		return "", fmt.Errorf("get local address format is invalid")
	}

	return items[0], nil
}
