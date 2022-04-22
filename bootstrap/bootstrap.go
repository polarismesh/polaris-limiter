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
	"os"
	"os/signal"
	"syscall"

	"github.com/polarismesh/polaris-limiter/apiserver"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/polarismesh/polaris-limiter/plugin"
	"github.com/polarismesh/polaris-limiter/ratelimitv2"
)

// bootstrap退出的封装
func bootExit(msg string) {
	fmt.Println(msg)
	os.Exit(-1)
}

// Start Server启动入口
// 引导启动阶段，遇到错误，直接退出
func Start(configPath string, restart bool) {
	servers, errCh, cancel := NonBlockingStart(configPath, restart)
	defer func() {
		cancel()
		plugin.GlobalDestroy()
	}()
	// 进入主循环
	runMainLoop(servers, errCh)
}

// NonBlockingStart 非阻塞启动
func NonBlockingStart(configPath string, restart bool) ([]apiserver.APIServer, chan error, context.CancelFunc) {
	config := loadConfig(configPath)
	fmt.Printf("load config: %+v\n", config)

	if len(config.APIServers) == 0 {
		bootExit("api servers is empty")
	}

	// log init
	options := &config.Logger
	_ = options.SetOutputLevel(log.DefaultScopeName, options.Level)
	options.SetStackTraceLevel(log.DefaultScopeName, "none")
	options.SetLogCallers(log.DefaultScopeName, true)
	if err := log.Configure(options); err != nil {
		bootExit(fmt.Sprintf("log init err: %s", err.Error()))
	}

	// plugin init
	plugin.GlobalInitialize(&config.Plugin)

	// ratelimit core server 初始化
	ctx, cancel := context.WithCancel(context.Background())

	if len(config.Registry.Host) > 0 {
		utils.ServerAddress = config.Registry.Host
	} else if len(config.Registry.PolarisServerAddress) > 0 {
		serverAddr, err := GetLocalHost(config.Registry.PolarisServerAddress)
		if err != nil {
			bootExit(fmt.Sprintf("[Bootstrap] get local host err: %s", err.Error()))
		}
		log.Infof("[Bootstrap] get local host: %s", serverAddr)
		utils.ServerAddress = serverAddr
	}
	utils.LimitServiceName = config.Registry.Name
	if err := ratelimitv2.Initialize(ctx, &config.Limit); err != nil {
		bootExit(fmt.Sprintf("ratelimitv2 core server initialize err: %s", err.Error()))
	}
	// 初始化智研上报
	// observer.Initialize(ctx, &config.Limit)

	// 启动server
	errCh := make(chan error, len(config.APIServers))
	servers := startServers(config, errCh)

	var err error
	registryCfg := &config.Registry
	if nil != registryCfg && registryCfg.Enable {
		if err = initPolarisClient(registryCfg); nil != err {
			bootExit(fmt.Sprintf("fail to init polaris sdk, err: %s", err.Error()))
		}
		// 服务注册
		if err := selfRegister(registryCfg, servers, utils.ServerAddress); err != nil {
			bootExit(fmt.Sprintf("service registry err: %s", err.Error()))
		}
		if registryCfg.HealthCheckEnable { // 启动心跳上报
			startHeartbeat(ctx)
		}
	} else {
		log.Info("[Bootstrap] not enable register the ratelimit server")
	}
	return servers, errCh, cancel
}

// 启动server
func startServers(config *Config, errCh chan error) []apiserver.APIServer {
	servers := make([]apiserver.APIServer, 0, len(config.APIServers))
	for _, entry := range config.APIServers {
		server, ok := apiserver.Slots[entry.Name]
		if !ok {
			bootExit(fmt.Sprintf("api server(%s) is not registry", entry.Name))
		}

		if err := server.Initialize(entry.Option); err != nil {
			bootExit(fmt.Sprintf("api server(%s) initialize err: %s", entry.Name, err.Error()))
		}

		servers = append(servers, server)
		go server.Run(errCh)
	}

	return servers
}

// 接受外部信号，停止server
func stopServers(servers []apiserver.APIServer) {
	// 先反注册所有服务
	if err := selfDeregister(); nil != err {
		log.Errorf("fail to selfDeregister, err: %s", err.Error())
	}

	// 停掉服务
	for _, s := range servers {
		log.Infof("stop server protocol: %s", s.GetProtocol())
		s.Stop()
	}
}

// 程序主循环
func runMainLoop(servers []apiserver.APIServer, errCh chan error) {
	sigCh := make(chan os.Signal)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGSEGV)
	for {
		select {
		case sig := <-sigCh:
			log.Infof("catch signal(%+v), stop servers", sig)
			stopServers(servers)
			return
		case err := <-errCh:
			log.Errorf("[Boot] catch server err: %s", err.Error())
			stopServers(servers)
			return
		}
	}
}
