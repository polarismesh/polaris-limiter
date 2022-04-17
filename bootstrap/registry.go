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
	"time"

	clientAPI "git.code.oa.com/polaris/polaris-go/api"
	clientConfig "git.code.oa.com/polaris/polaris-go/pkg/config"
	"github.com/polarismesh/polaris-limit/apiserver"
	"github.com/polarismesh/polaris-limit/pkg/log"
	"github.com/polarismesh/polaris-limit/pkg/version"
)

//心跳上报周期
const serverTtl = 2 * time.Second

var (
	clientSDK            clientAPI.ProviderAPI
	heartBeatRequestChan chan *clientAPI.InstanceHeartbeatRequest
	selfServiceInstances = make([]*clientAPI.InstanceDeRegisterRequest, 0)
)

//初始化客户端SDK
func initPolarisClient(registryCfg *Registry) (err error) {
	var cfg clientConfig.Configuration
	serverAddress := registryCfg.PolarisServerAddress
	if len(serverAddress) > 0 {
		cfg = clientConfig.NewDefaultConfiguration([]string{serverAddress})
	} else {
		cfg = clientConfig.NewDefaultConfigurationWithDomain()
	}
	cfg.GetGlobal().GetStatReporter().SetEnable(false)
	cfg.GetGlobal().GetAPI().SetMaxRetryTimes(1)
	cfg.GetConsumer().GetLocalCache().SetPersistDir("./polaris/backup")
	clientSDK, err = clientAPI.NewProviderAPIByConfig(cfg)
	return
}

//启动心跳上报
func startHeartbeat(ctx context.Context) {
	heartBeatRequestChan = make(chan *clientAPI.InstanceHeartbeatRequest)
	go func() {
		ticker := time.NewTicker(serverTtl)
		defer ticker.Stop()
		var heartBeatRequests []*clientAPI.InstanceHeartbeatRequest
		for {
			select {
			case <-ctx.Done():
				log.Infof("[Bootstrap] heartbeat routine stopped")
				return
			case <-ticker.C:
				if len(heartBeatRequests) == 0 {
					continue
				}
				for _, req := range heartBeatRequests {
					err := clientSDK.Heartbeat(req)
					if nil != err {
						log.Errorf("[Bootstrap] fail to send heatBeat, err is %v", err)
					}
				}
			case req := <-heartBeatRequestChan:
				heartBeatRequests = append(heartBeatRequests, req)
			}
		}
	}()
}

//创建服务注册请求
func buildRegisterRequest(
	cfg *Registry, server apiserver.APIServer, serverAddress string, restart bool) *clientAPI.InstanceRegisterRequest {
	request := &clientAPI.InstanceRegisterRequest{}
	request.Namespace = cfg.Namespace
	request.Service = cfg.Name
	request.Port = int(server.GetPort())
	request.Host = serverAddress
	request.ServiceToken = cfg.Token
	protocol := server.GetProtocol()
	request.Protocol = &protocol
	request.Version = &version.Version
	request.Metadata = map[string]string{"build-revision": version.GetRevision()}
	if cfg.HealthCheckEnable { // 开启健康检查
		request.SetTTL(int(serverTtl / time.Second))
	}
	if !restart {
		request.SetIsolate(true)
	}
	return request
}

// 注册限流Server
func selfRegister(cfg *Registry, servers []apiserver.APIServer, serverAddress string, restart bool) error {
	// 开始对每个监听端口的服务进行注册
	for _, server := range servers {
		req := buildRegisterRequest(cfg, server, serverAddress, restart)
		resp, err := clientSDK.Register(req)
		if nil != err {
			log.Errorf("[Bootstrap] fail to register instance, err: %s", err.Error())
			return err
		}
		log.Infof(
			"[Bootstrap] instance %d:%s registered, id %s", serverAddress, server.GetPort(), resp.InstanceID)
		deregisterReq := &clientAPI.InstanceDeRegisterRequest{}
		deregisterReq.Namespace = req.Namespace
		deregisterReq.Service = req.Service
		deregisterReq.Host = req.Host
		deregisterReq.Port = req.Port
		deregisterReq.ServiceToken = cfg.Token
		if resp.Existed {
			log.Infof("[Bootstrap] instance %s:%d exists, start to deregister", serverAddress, server.GetPort())
			//已经存在，则反注册
			if err = clientSDK.Deregister(deregisterReq); nil != err {
				log.Errorf("[Bootstrap] fail to deregister instance, err: %s", err.Error())
				return err
			}
			log.Infof("[Bootstrap] start to re-register instance %s:%d, ", serverAddress, server.GetPort())
			resp, err = clientSDK.Register(req)
			if nil != err {
				log.Errorf("[Bootstrap] fail to re-register instance, err: %s", err.Error())
				return err
			}
		}
		selfServiceInstances = append(selfServiceInstances, deregisterReq)
		if cfg.HealthCheckEnable {
			healthCheckReq := &clientAPI.InstanceHeartbeatRequest{}
			healthCheckReq.InstanceID = resp.InstanceID
			healthCheckReq.ServiceToken = cfg.Token
			heartBeatRequestChan <- healthCheckReq
		}
	}
	return nil
}

// 反注册
func selfDeregister() error {
	if len(selfServiceInstances) == 0 {
		return nil
	}
	var err error
	for _, instance := range selfServiceInstances {
		err = clientSDK.Deregister(instance)
		if err != nil {
			log.Errorf("[Bootstrap] fail to deregister instance err: %s", err.Error())
		}
	}
	return err
}

// 获取本地IP地址
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
