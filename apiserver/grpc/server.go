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

package grpc

import (
	"fmt"
	"net"

	"github.com/polarismesh/specification/source/go/api/v1/traffic_manage/ratelimiter"
	"golang.org/x/net/netutil"
	"google.golang.org/grpc"

	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/plugin"
	"github.com/polarismesh/polaris-limiter/ratelimitv2"
)

// Server grpc server
type Server struct {
	IP                 string
	Port               uint32
	MaxConnections     int
	server             *grpc.Server
	rateLimitServiceV2 *RateLimitServiceV2
}

// Initialize 初始化函数
func (g *Server) Initialize(option map[string]interface{}) error {
	g.IP = option["ip"].(string)
	g.Port = uint32(option["port"].(int))
	if v, ok := option["max-connections"]; ok {
		g.MaxConnections = v.(int)
	}
	return nil
}

// GetProtocol 返回协议
func (g *Server) GetProtocol() string {
	return "grpc"
}

// GetPort 返回port
func (g *Server) GetPort() uint32 {
	return g.Port
}

// Run 入口函数
func (g *Server) Run(errCh chan error) {
	// 初始化grpc server监听listener
	address := fmt.Sprintf("%s:%d", g.IP, g.Port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Errorf("[GRPC] grpc listen(%s) err: %s", address, err.Error())
		errCh <- err
		return
	}

	// 限制最大TCP连接数
	if g.MaxConnections > 0 {
		listener = netutil.LimitListener(listener, g.MaxConnections)
		log.Infof("[GRPC] max connections limit: %d", g.MaxConnections)
	}

	g.rateLimitServiceV2 = &RateLimitServiceV2{}
	// 创建grpc server
	server := grpc.NewServer(
		grpc.UnaryInterceptor(g.unaryInterceptor),
		grpc.StreamInterceptor(g.streamInterceptor),
	)
	ratelimiter.RegisterRateLimitGRPCV2Server(server, g.rateLimitServiceV2)
	g.server = server

	serviceInfos := server.GetServiceInfo()
	for key, serviceInfo := range serviceInfos {
		log.Infof("register service key %s, info is %+v", key, serviceInfo)
	}

	ratelimitServerv2, err := ratelimitv2.GetRateLimitServer()
	if err != nil {
		log.Errorf("[GRPC] grpc get rateLimitServerV2 server err: %s", err.Error())
		errCh <- err
		return
	}
	g.rateLimitServiceV2.coreServer = ratelimitServerv2

	// 获取插件
	statis, err := plugin.GetStatis()
	if err != nil {
		log.Errorf("[GRPC] grpc plugin get statis err: %s", err.Error())
		errCh <- err
		return
	}
	g.rateLimitServiceV2.statics = statis
	ratelimitv2.SetStatics(statis)

	// 绑定到连接中，并开始监听server
	if err := g.server.Serve(listener); err != nil {
		log.Errorf("[GRPC] grpc server Serve err: %s", err.Error())
		errCh <- err
		return
	}
}

// Stop 停止
func (g *Server) Stop() {
	if g.server != nil {
		g.server.Stop()
	}
}
