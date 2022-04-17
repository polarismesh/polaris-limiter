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

package ratelimitv2

import (
	"context"
	"errors"
	"github.com/polarismesh/polaris-limit/pkg/config"
	"github.com/polarismesh/polaris-limit/plugin"
	"sync"
)

var (
	server     = new(Server)
	once       = sync.Once{}
	finishInit = false
	statics    plugin.Statis
)

//设置统计插件
func SetStatics(loadStatics plugin.Statis) {
	statics = loadStatics
}

//v2版本的主server逻辑
type Server struct {
	counterMng *CounterManagerV2
	clientMng  *ClientManager
	cfg        config.Config
}

//获取计数器管理类
func (s *Server) CounterMng() *CounterManagerV2 {
	return s.counterMng
}

//清理客户端
func (s *Server) CleanupClient(client Client, streamCtxId string) {
	if s.clientMng.DelClient(client, streamCtxId) {
		client.Cleanup()
	}
}

// 获取已经初始化好的Server
func GetRateLimitServer() (*Server, error) {
	if !finishInit {
		return nil, errors.New("server has not done initialize")
	}

	return server, nil
}

// 初始化函数
func Initialize(ctx context.Context, config *config.Config) error {
	var err error
	once.Do(func() {
		err = initialize(ctx, config)
	})

	if err != nil {
		return err
	}

	finishInit = true
	return nil
}

// 内部初始化函数
func initialize(ctx context.Context, cfg *config.Config) error {
	var err error
	var pushManager PushManager
	newConfig, err := config.ParseConfig(cfg)
	if nil != err {
		return err
	}
	pushManager, err = NewPushManager(int(cfg.PushWorker), 1000)
	if nil != err {
		return err
	}
	server.cfg = *newConfig
	pushManager.Run(ctx)
	server.counterMng = NewCounterManagerV2(newConfig.MaxCounter, cfg.PurgeCounterInterval, pushManager)
	server.clientMng = NewClientManager(newConfig.MaxClient)
	server.counterMng.Start(ctx)
	return nil
}
