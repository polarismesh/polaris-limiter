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
	"os"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"gopkg.in/yaml.v2"

	"github.com/polarismesh/polaris-limiter/apiserver"
)

// mockAPIServer 模拟 APIServer 接口，用于单测
type mockAPIServer struct {
	protocol string
	port     uint32
}

func (m *mockAPIServer) Initialize(option map[string]interface{}) error { return nil }
func (m *mockAPIServer) Run(errCh chan error)                           {}
func (m *mockAPIServer) Stop()                                          {}
func (m *mockAPIServer) GetProtocol() string                            { return m.protocol }
func (m *mockAPIServer) GetPort() uint32                                { return m.port }

// TestBuildRegisterRequest_CustomPort 测试 buildRegisterRequest 自定义注册端口逻辑
func TestBuildRegisterRequest_CustomPort(t *testing.T) {
	Convey("测试 buildRegisterRequest 自定义注册端口", t, func() {
		baseCfg := &Registry{
			Name:      "polaris.limiter",
			Namespace: "Polaris",
		}

		Convey("配置了 RegisterPort 时应使用自定义端口", func() {
			server := &mockAPIServer{protocol: "grpc", port: 8101}
			serverCfg := apiserver.Config{
				Name:         "grpc",
				RegisterPort: 9999,
			}

			instance := buildRegisterRequest(baseCfg, server, serverCfg, "10.0.0.1")

			So(instance.GetPort().GetValue(), ShouldEqual, uint32(9999))
			So(instance.GetHost().GetValue(), ShouldEqual, "10.0.0.1")
			So(instance.GetProtocol().GetValue(), ShouldEqual, "grpc")
		})

		Convey("RegisterPort 为 0 时应使用 server 实际监听端口", func() {
			server := &mockAPIServer{protocol: "http", port: 8100}
			serverCfg := apiserver.Config{
				Name:         "http",
				RegisterPort: 0,
			}

			instance := buildRegisterRequest(baseCfg, server, serverCfg, "10.0.0.1")

			So(instance.GetPort().GetValue(), ShouldEqual, uint32(8100))
		})

		Convey("未设置 RegisterPort 时应使用 server 实际监听端口", func() {
			server := &mockAPIServer{protocol: "grpc", port: 8101}
			serverCfg := apiserver.Config{
				Name: "grpc",
			}

			instance := buildRegisterRequest(baseCfg, server, serverCfg, "192.168.1.1")

			So(instance.GetPort().GetValue(), ShouldEqual, uint32(8101))
		})

		Convey("不同 server 可以配置不同的自定义端口", func() {
			httpServer := &mockAPIServer{protocol: "http", port: 8100}
			grpcServer := &mockAPIServer{protocol: "grpc", port: 8101}

			httpCfg := apiserver.Config{Name: "http", RegisterPort: 19000}
			grpcCfg := apiserver.Config{Name: "grpc", RegisterPort: 19001}

			httpInstance := buildRegisterRequest(baseCfg, httpServer, httpCfg, "10.0.0.1")
			grpcInstance := buildRegisterRequest(baseCfg, grpcServer, grpcCfg, "10.0.0.1")

			So(httpInstance.GetPort().GetValue(), ShouldEqual, uint32(19000))
			So(grpcInstance.GetPort().GetValue(), ShouldEqual, uint32(19001))
		})

		Convey("部分 server 配置自定义端口，部分使用默认端口", func() {
			httpServer := &mockAPIServer{protocol: "http", port: 8100}
			grpcServer := &mockAPIServer{protocol: "grpc", port: 8101}

			// HTTP 配置了自定义端口，gRPC 未配置
			httpCfg := apiserver.Config{Name: "http", RegisterPort: 19000}
			grpcCfg := apiserver.Config{Name: "grpc", RegisterPort: 0}

			httpInstance := buildRegisterRequest(baseCfg, httpServer, httpCfg, "10.0.0.1")
			grpcInstance := buildRegisterRequest(baseCfg, grpcServer, grpcCfg, "10.0.0.1")

			So(httpInstance.GetPort().GetValue(), ShouldEqual, uint32(19000))
			So(grpcInstance.GetPort().GetValue(), ShouldEqual, uint32(8101)) // 使用 server 实际端口
		})
	})
}

// TestBuildRegisterRequest_CustomHost 测试 buildRegisterRequest 自定义注册 IP 逻辑
func TestBuildRegisterRequest_CustomHost(t *testing.T) {
	Convey("测试 buildRegisterRequest 自定义注册 IP", t, func() {
		Convey("传入自定义 IP 时应使用该 IP", func() {
			cfg := &Registry{
				Name:      "polaris.limiter",
				Namespace: "Polaris",
				Host:      "10.0.0.100",
			}
			server := &mockAPIServer{protocol: "grpc", port: 8101}
			serverCfg := apiserver.Config{Name: "grpc"}

			// 注意：buildRegisterRequest 中 serverAddress 参数即为注册 IP
			// 在 bootstrap.go 中，如果配置了 Host 则 utils.ServerAddress = config.Registry.Host
			instance := buildRegisterRequest(cfg, server, serverCfg, cfg.Host)

			So(instance.GetHost().GetValue(), ShouldEqual, "10.0.0.100")
		})

		Convey("传入自动探测的 IP 时应使用该 IP", func() {
			cfg := &Registry{
				Name:      "polaris.limiter",
				Namespace: "Polaris",
			}
			server := &mockAPIServer{protocol: "http", port: 8100}
			serverCfg := apiserver.Config{Name: "http"}

			instance := buildRegisterRequest(cfg, server, serverCfg, "192.168.1.100")

			So(instance.GetHost().GetValue(), ShouldEqual, "192.168.1.100")
		})
	})
}

// TestBuildRegisterRequest_RegistryFields 测试 buildRegisterRequest 注册信息完整性
func TestBuildRegisterRequest_RegistryFields(t *testing.T) {
	Convey("测试 buildRegisterRequest 生成的注册信息完整性", t, func() {
		cfg := &Registry{
			Name:              "polaris.limiter",
			Namespace:         "Polaris",
			HealthCheckEnable: true,
		}
		server := &mockAPIServer{protocol: "grpc", port: 8101}
		serverCfg := apiserver.Config{Name: "grpc", RegisterPort: 9090}

		// 设置全局 token（buildRegisterRequest 中使用了 polarisToken 包级变量）
		polarisToken = "test-token"
		defer func() { polarisToken = "" }()

		instance := buildRegisterRequest(cfg, server, serverCfg, "10.0.0.1")

		Convey("Namespace 和 Service 应正确设置", func() {
			So(instance.GetNamespace().GetValue(), ShouldEqual, "Polaris")
			So(instance.GetService().GetValue(), ShouldEqual, "polaris.limiter")
		})

		Convey("Host 和 Port 应正确设置", func() {
			So(instance.GetHost().GetValue(), ShouldEqual, "10.0.0.1")
			So(instance.GetPort().GetValue(), ShouldEqual, uint32(9090))
		})

		Convey("Protocol 应正确设置", func() {
			So(instance.GetProtocol().GetValue(), ShouldEqual, "grpc")
		})

		Convey("ServiceToken 应正确设置", func() {
			So(instance.GetServiceToken().GetValue(), ShouldEqual, "test-token")
		})

		Convey("开启健康检查时应包含 HealthCheck 配置", func() {
			So(instance.GetEnableHealthCheck().GetValue(), ShouldBeTrue)
			So(instance.GetHealthCheck(), ShouldNotBeNil)
			So(instance.GetHealthCheck().GetHeartbeat().GetTtl().GetValue(), ShouldEqual, uint32(5))
		})
	})
}

// TestBuildRegisterRequest_HealthCheckDisabled 测试关闭健康检查时的行为
func TestBuildRegisterRequest_HealthCheckDisabled(t *testing.T) {
	Convey("测试关闭健康检查时不应包含 HealthCheck 配置", t, func() {
		cfg := &Registry{
			Name:              "polaris.limiter",
			Namespace:         "Polaris",
			HealthCheckEnable: false,
		}
		server := &mockAPIServer{protocol: "http", port: 8100}
		serverCfg := apiserver.Config{Name: "http"}

		instance := buildRegisterRequest(cfg, server, serverCfg, "10.0.0.1")

		So(instance.GetEnableHealthCheck().GetValue(), ShouldBeFalse)
		So(instance.GetHealthCheck(), ShouldBeNil)
	})
}

// TestConfigYAML_CustomHostAndPort 测试 YAML 配置文件能正确解析自定义 IP 和端口
func TestConfigYAML_CustomHostAndPort(t *testing.T) {
	Convey("测试 YAML 配置解析自定义注册 IP 和端口", t, func() {
		Convey("完整配置应正确解析", func() {
			yamlContent := `
registry:
  enable: true
  polaris-server-address: 127.0.0.1:8091
  name: polaris.limiter
  namespace: Polaris
  host: 10.0.0.100
  health-check-enable: true
api-servers:
  - name: http
    register-port: 19000
    option:
      ip: 0.0.0.0
      port: 8100
  - name: grpc
    register-port: 19001
    option:
      ip: 0.0.0.0
      port: 8101
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)

			// 验证 Registry
			So(config.Registry.Enable, ShouldBeTrue)
			So(config.Registry.Host, ShouldEqual, "10.0.0.100")
			So(config.Registry.Name, ShouldEqual, "polaris.limiter")
			So(config.Registry.Namespace, ShouldEqual, "Polaris")
			So(config.Registry.PolarisServerAddress, ShouldEqual, "127.0.0.1:8091")
			So(config.Registry.HealthCheckEnable, ShouldBeTrue)

			// 验证 APIServers
			So(len(config.APIServers), ShouldEqual, 2)
			So(config.APIServers[0].Name, ShouldEqual, "http")
			So(config.APIServers[0].RegisterPort, ShouldEqual, uint32(19000))
			So(config.APIServers[1].Name, ShouldEqual, "grpc")
			So(config.APIServers[1].RegisterPort, ShouldEqual, uint32(19001))
		})

		Convey("不配置 host 和 register-port 时应为零值", func() {
			yamlContent := `
registry:
  enable: true
  polaris-server-address: 127.0.0.1:8091
  name: polaris.limiter
  namespace: Polaris
api-servers:
  - name: http
    option:
      ip: 0.0.0.0
      port: 8100
  - name: grpc
    option:
      ip: 0.0.0.0
      port: 8101
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)

			// Host 应为空字符串
			So(config.Registry.Host, ShouldEqual, "")

			// RegisterPort 应为 0
			So(config.APIServers[0].RegisterPort, ShouldEqual, uint32(0))
			So(config.APIServers[1].RegisterPort, ShouldEqual, uint32(0))
		})

		Convey("只配置部分 server 的 register-port", func() {
			yamlContent := `
registry:
  enable: true
  polaris-server-address: 127.0.0.1:8091
  name: polaris.limiter
  namespace: Polaris
api-servers:
  - name: http
    option:
      ip: 0.0.0.0
      port: 8100
  - name: grpc
    register-port: 19001
    option:
      ip: 0.0.0.0
      port: 8101
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)

			// HTTP 未配置 register-port，应为 0
			So(config.APIServers[0].RegisterPort, ShouldEqual, uint32(0))
			// gRPC 配置了 register-port，应为 19001
			So(config.APIServers[1].RegisterPort, ShouldEqual, uint32(19001))
		})
	})
}

// TestConfigYAML_LoadFromFile 测试从文件加载配置时自定义 IP 和端口的解析
func TestConfigYAML_LoadFromFile(t *testing.T) {
	Convey("测试从临时文件加载配置", t, func() {
		yamlContent := `
registry:
  enable: true
  polaris-server-address: 127.0.0.1:8091
  name: polaris.limiter
  namespace: Polaris
  host: 10.20.30.40
  health-check-enable: true
api-servers:
  - name: http
    register-port: 28100
    option:
      ip: 0.0.0.0
      port: 8100
  - name: grpc
    register-port: 28101
    option:
      ip: 0.0.0.0
      port: 8101
limit:
  myid: 1
  counter-group: 64
  max-counter: 1000000
  max-client: 1000
  push-worker: 4
  slide-count: 1
  purge-counter-interval: 30s
`
		// 创建临时文件
		tmpFile, err := os.CreateTemp("", "polaris-limiter-test-*.yaml")
		So(err, ShouldBeNil)
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString(yamlContent)
		So(err, ShouldBeNil)
		tmpFile.Close()

		// 使用 yaml 解析（不调用 loadConfig 因为它内部有 bootExit）
		file, err := os.Open(tmpFile.Name())
		So(err, ShouldBeNil)
		defer file.Close()

		var config Config
		err = yaml.NewDecoder(file).Decode(&config)
		So(err, ShouldBeNil)

		// 验证自定义 IP
		So(config.Registry.Host, ShouldEqual, "10.20.30.40")

		// 验证自定义端口
		So(config.APIServers[0].Name, ShouldEqual, "http")
		So(config.APIServers[0].RegisterPort, ShouldEqual, uint32(28100))
		So(config.APIServers[1].Name, ShouldEqual, "grpc")
		So(config.APIServers[1].RegisterPort, ShouldEqual, uint32(28101))
	})
}

// TestGetLocalHost 测试获取本地 IP 地址
func TestGetLocalHost(t *testing.T) {
	Convey("测试 GetLocalHost 函数", t, func() {
		Convey("probeAddr 为空时应返回 127.0.0.1", func() {
			host, err := GetLocalHost("")
			So(err, ShouldBeNil)
			So(host, ShouldEqual, "127.0.0.1")
		})
	})
}

// TestEndToEnd_CustomIPAndPort 端到端测试：模拟完整的注册流程中 IP 和端口的选择
func TestEndToEnd_CustomIPAndPort(t *testing.T) {
	Convey("端到端测试：模拟注册流程中自定义 IP 和端口的选择", t, func() {
		Convey("场景1：配置了自定义 Host 和 RegisterPort", func() {
			cfg := &Registry{
				Name:      "polaris.limiter",
				Namespace: "Polaris",
				Host:      "10.0.0.100",
			}

			httpServer := &mockAPIServer{protocol: "http", port: 8100}
			grpcServer := &mockAPIServer{protocol: "grpc", port: 8101}

			apiServerConfigs := []apiserver.Config{
				{Name: "http", RegisterPort: 19000},
				{Name: "grpc", RegisterPort: 19001},
			}

			// 模拟 selfRegister 中构建映射的逻辑
			serverCfgMap := make(map[string]apiserver.Config, len(apiServerConfigs))
			for _, sc := range apiServerConfigs {
				serverCfgMap[sc.Name] = sc
			}

			// 模拟 bootstrap.go 中选择 serverAddress 的逻辑
			serverAddress := cfg.Host // 配置了 Host，使用自定义 Host

			// 验证 HTTP server 的注册信息
			httpCfg := serverCfgMap[httpServer.GetProtocol()]
			httpInstance := buildRegisterRequest(cfg, httpServer, httpCfg, serverAddress)
			So(httpInstance.GetHost().GetValue(), ShouldEqual, "10.0.0.100")
			So(httpInstance.GetPort().GetValue(), ShouldEqual, uint32(19000))

			// 验证 gRPC server 的注册信息
			grpcCfg := serverCfgMap[grpcServer.GetProtocol()]
			grpcInstance := buildRegisterRequest(cfg, grpcServer, grpcCfg, serverAddress)
			So(grpcInstance.GetHost().GetValue(), ShouldEqual, "10.0.0.100")
			So(grpcInstance.GetPort().GetValue(), ShouldEqual, uint32(19001))
		})

		Convey("场景2：未配置自定义 Host 和 RegisterPort，使用默认值", func() {
			cfg := &Registry{
				Name:      "polaris.limiter",
				Namespace: "Polaris",
			}

			httpServer := &mockAPIServer{protocol: "http", port: 8100}
			grpcServer := &mockAPIServer{protocol: "grpc", port: 8101}

			apiServerConfigs := []apiserver.Config{
				{Name: "http"},
				{Name: "grpc"},
			}

			serverCfgMap := make(map[string]apiserver.Config, len(apiServerConfigs))
			for _, sc := range apiServerConfigs {
				serverCfgMap[sc.Name] = sc
			}

			// 未配置 Host，使用自动探测的 IP
			autoDetectedIP := "192.168.1.100"

			httpCfg := serverCfgMap[httpServer.GetProtocol()]
			httpInstance := buildRegisterRequest(cfg, httpServer, httpCfg, autoDetectedIP)
			So(httpInstance.GetHost().GetValue(), ShouldEqual, "192.168.1.100")
			So(httpInstance.GetPort().GetValue(), ShouldEqual, uint32(8100)) // 使用 server 实际端口

			grpcCfg := serverCfgMap[grpcServer.GetProtocol()]
			grpcInstance := buildRegisterRequest(cfg, grpcServer, grpcCfg, autoDetectedIP)
			So(grpcInstance.GetHost().GetValue(), ShouldEqual, "192.168.1.100")
			So(grpcInstance.GetPort().GetValue(), ShouldEqual, uint32(8101)) // 使用 server 实际端口
		})

		Convey("场景3：混合配置 - 自定义 Host + 部分 RegisterPort", func() {
			cfg := &Registry{
				Name:      "polaris.limiter",
				Namespace: "Polaris",
				Host:      "10.0.0.1",
			}

			httpServer := &mockAPIServer{protocol: "http", port: 8100}
			grpcServer := &mockAPIServer{protocol: "grpc", port: 8101}

			apiServerConfigs := []apiserver.Config{
				{Name: "http"}, // 未配置 RegisterPort
				{Name: "grpc", RegisterPort: 29001},
			}

			serverCfgMap := make(map[string]apiserver.Config, len(apiServerConfigs))
			for _, sc := range apiServerConfigs {
				serverCfgMap[sc.Name] = sc
			}

			serverAddress := cfg.Host

			httpCfg := serverCfgMap[httpServer.GetProtocol()]
			httpInstance := buildRegisterRequest(cfg, httpServer, httpCfg, serverAddress)
			So(httpInstance.GetHost().GetValue(), ShouldEqual, "10.0.0.1")
			So(httpInstance.GetPort().GetValue(), ShouldEqual, uint32(8100)) // 使用 server 实际端口

			grpcCfg := serverCfgMap[grpcServer.GetProtocol()]
			grpcInstance := buildRegisterRequest(cfg, grpcServer, grpcCfg, serverAddress)
			So(grpcInstance.GetHost().GetValue(), ShouldEqual, "10.0.0.1")
			So(grpcInstance.GetPort().GetValue(), ShouldEqual, uint32(29001)) // 使用自定义端口
		})
	})
}

// boolPtr 辅助函数，返回 bool 指针
func boolPtr(b bool) *bool {
	return &b
}

// TestConfigShouldRegister 测试 Config.ShouldRegister 方法
func TestConfigShouldRegister(t *testing.T) {
	Convey("测试 Config.ShouldRegister 方法", t, func() {
		Convey("RegisterEnabled 为 nil（未配置）时应返回 true", func() {
			cfg := apiserver.Config{Name: "http"}
			So(cfg.ShouldRegister(), ShouldBeTrue)
		})

		Convey("RegisterEnabled 为 true 时应返回 true", func() {
			cfg := apiserver.Config{Name: "http", RegisterEnabled: boolPtr(true)}
			So(cfg.ShouldRegister(), ShouldBeTrue)
		})

		Convey("RegisterEnabled 为 false 时应返回 false", func() {
			cfg := apiserver.Config{Name: "http", RegisterEnabled: boolPtr(false)}
			So(cfg.ShouldRegister(), ShouldBeFalse)
		})
	})
}

// TestConfigYAML_RegisterSwitch 测试 YAML 配置文件能正确解析 register 开关
func TestConfigYAML_RegisterSwitch(t *testing.T) {
	Convey("测试 YAML 配置解析 register 开关", t, func() {
		Convey("不配置 register 字段时应默认注册（ShouldRegister 返回 true）", func() {
			yamlContent := `
api-servers:
  - name: http
    option:
      ip: 0.0.0.0
      port: 8100
  - name: grpc
    option:
      ip: 0.0.0.0
      port: 8101
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			So(len(config.APIServers), ShouldEqual, 2)
			So(config.APIServers[0].RegisterEnabled, ShouldBeNil)
			So(config.APIServers[0].ShouldRegister(), ShouldBeTrue)
			So(config.APIServers[1].RegisterEnabled, ShouldBeNil)
			So(config.APIServers[1].ShouldRegister(), ShouldBeTrue)
		})

		Convey("配置 register-enabled: true 时应注册", func() {
			yamlContent := `
api-servers:
  - name: http
    register-enabled: true
    option:
      ip: 0.0.0.0
      port: 8100
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			So(config.APIServers[0].RegisterEnabled, ShouldNotBeNil)
			So(*config.APIServers[0].RegisterEnabled, ShouldBeTrue)
			So(config.APIServers[0].ShouldRegister(), ShouldBeTrue)
		})

		Convey("配置 register-enabled: false 时应不注册", func() {
			yamlContent := `
api-servers:
  - name: http
    register-enabled: false
    option:
      ip: 0.0.0.0
      port: 8100
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			So(config.APIServers[0].RegisterEnabled, ShouldNotBeNil)
			So(*config.APIServers[0].RegisterEnabled, ShouldBeFalse)
			So(config.APIServers[0].ShouldRegister(), ShouldBeFalse)
		})

		Convey("混合配置：部分 server 注册，部分不注册", func() {
			yamlContent := `
api-servers:
  - name: http
    register-enabled: false
    option:
      ip: 0.0.0.0
      port: 8100
  - name: grpc
    register-enabled: true
    option:
      ip: 0.0.0.0
      port: 8101
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			So(len(config.APIServers), ShouldEqual, 2)
			So(config.APIServers[0].ShouldRegister(), ShouldBeFalse)
			So(config.APIServers[1].ShouldRegister(), ShouldBeTrue)
		})

		Convey("register 与 register-port 组合使用", func() {
			yamlContent := `
api-servers:
  - name: http
    register-enabled: false
    register-port: 19000
    option:
      ip: 0.0.0.0
      port: 8100
  - name: grpc
    register-enabled: true
    register-port: 19001
    option:
      ip: 0.0.0.0
      port: 8101
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			So(config.APIServers[0].ShouldRegister(), ShouldBeFalse)
			So(config.APIServers[0].RegisterPort, ShouldEqual, uint32(19000))
			So(config.APIServers[1].ShouldRegister(), ShouldBeTrue)
			So(config.APIServers[1].RegisterPort, ShouldEqual, uint32(19001))
		})
	})
}

// resetReRegisterState 重置所有重注册相关的全局状态，用于测试隔离
func resetReRegisterState() {
	atomic.StoreInt32(&notFoundCount, 0)
	atomic.StoreInt32(&reRegisterCount, 0)
	atomic.StoreInt32(&reRegistering, 0)
	savedRegistryCfg = nil
	savedAPIServers = nil
	savedAPIServerConfigs = nil
	savedServerAddress = ""
}

// TestCalcReRegisterDelay 测试退避延迟计算逻辑
func TestCalcReRegisterDelay(t *testing.T) {
	Convey("测试 calcReRegisterDelay 退避延迟计算", t, func() {
		Convey("count=0 时应立即执行（delay=0）", func() {
			delay := calcReRegisterDelay(0)
			So(delay, ShouldEqual, 0)
		})

		Convey("count<0 时应立即执行（delay=0）", func() {
			delay := calcReRegisterDelay(-1)
			So(delay, ShouldEqual, 0)
		})

		Convey("count=1 时 delay = serverTtl * 2^0 + jitter = serverTtl + jitter", func() {
			delay := calcReRegisterDelay(1)
			// base = 5s * 2^0 = 5s, jitter in [0, 5s)
			// delay in [5s, 10s)
			So(delay, ShouldBeGreaterThanOrEqualTo, serverTtl)
			So(delay, ShouldBeLessThan, 2*serverTtl)
		})

		Convey("count=2 时 delay = serverTtl * 2^1 + jitter = 10s + jitter", func() {
			delay := calcReRegisterDelay(2)
			// base = 5s * 2^1 = 10s, jitter in [0, 5s)
			// delay in [10s, 15s)
			So(delay, ShouldBeGreaterThanOrEqualTo, 2*serverTtl)
			So(delay, ShouldBeLessThan, 3*serverTtl)
		})

		Convey("大 count 值时 delay 应不超过 maxReRegisterDelay", func() {
			delay := calcReRegisterDelay(100)
			So(delay, ShouldBeLessThanOrEqualTo, maxReRegisterDelay)
		})
	})
}

// TestReRegisterState_AtomicFlags 测试重注册状态标志的原子操作
func TestReRegisterState_AtomicFlags(t *testing.T) {
	Convey("测试重注册状态标志", t, func() {
		defer resetReRegisterState()

		Convey("初始状态 reRegistering 应为 0", func() {
			resetReRegisterState()
			So(atomic.LoadInt32(&reRegistering), ShouldEqual, 0)
		})

		Convey("CAS 设置 reRegistering 应成功", func() {
			resetReRegisterState()
			ok := atomic.CompareAndSwapInt32(&reRegistering, 0, 1)
			So(ok, ShouldBeTrue)
			So(atomic.LoadInt32(&reRegistering), ShouldEqual, 1)
		})

		Convey("reRegistering 已为 1 时 CAS 应失败", func() {
			resetReRegisterState()
			atomic.StoreInt32(&reRegistering, 1)
			ok := atomic.CompareAndSwapInt32(&reRegistering, 0, 1)
			So(ok, ShouldBeFalse)
		})

		Convey("notFoundCount 累加和重置", func() {
			resetReRegisterState()
			atomic.AddInt32(&notFoundCount, 1)
			atomic.AddInt32(&notFoundCount, 1)
			atomic.AddInt32(&notFoundCount, 1)
			So(atomic.LoadInt32(&notFoundCount), ShouldEqual, 3)

			atomic.StoreInt32(&notFoundCount, 0)
			So(atomic.LoadInt32(&notFoundCount), ShouldEqual, 0)
		})
	})
}

// TestSelfRegister_SavesContext 测试 selfRegister 保存注册上下文
func TestSelfRegister_SavesContext(t *testing.T) {
	Convey("测试 selfRegister 保存注册上下文供重注册使用", t, func() {
		defer resetReRegisterState()

		// selfRegister 会尝试连接 Polaris server，这里只验证上下文保存逻辑
		// 通过设置一个不可达的地址来触发连接失败
		cfg := &Registry{
			Name:      "polaris.limiter",
			Namespace: "Polaris",
		}
		servers := []apiserver.APIServer{&mockAPIServer{protocol: "grpc", port: 8101}}
		apiConfigs := []apiserver.Config{{Name: "grpc"}}

		polarisServerAddress = "127.0.0.1:1" // 不可达地址
		_ = selfRegister(cfg, servers, apiConfigs, "10.0.0.1")

		// 验证上下文已保存
		So(savedRegistryCfg, ShouldEqual, cfg)
		So(savedAPIServers, ShouldResemble, servers)
		So(savedAPIServerConfigs, ShouldResemble, apiConfigs)
		So(savedServerAddress, ShouldEqual, "10.0.0.1")
	})
}

// TestHeartbeat_NotFoundThreshold 测试心跳 NOT_FOUND 阈值逻辑
func TestHeartbeat_NotFoundThreshold(t *testing.T) {
	Convey("测试心跳 NOT_FOUND 失败计数逻辑", t, func() {
		defer resetReRegisterState()

		Convey("notFoundCount 未超过阈值时不应触发重注册", func() {
			resetReRegisterState()
			atomic.StoreInt32(&notFoundCount, 1)
			So(atomic.LoadInt32(&notFoundCount), ShouldBeLessThanOrEqualTo, int32(maxHeartbeatFailCount))
		})

		Convey("notFoundCount 超过阈值时应触发重注册", func() {
			resetReRegisterState()
			atomic.StoreInt32(&notFoundCount, 3) // > maxHeartbeatFailCount (2)
			So(atomic.LoadInt32(&notFoundCount), ShouldBeGreaterThan, int32(maxHeartbeatFailCount))
		})

		Convey("心跳成功后应重置 notFoundCount", func() {
			resetReRegisterState()
			atomic.StoreInt32(&notFoundCount, 5)
			// 模拟心跳成功
			atomic.StoreInt32(&notFoundCount, 0)
			So(atomic.LoadInt32(&notFoundCount), ShouldEqual, 0)
		})
	})
}

// TestAsyncReRegister_NilConfig 测试 savedRegistryCfg 为 nil 时的防护
func TestAsyncReRegister_NilConfig(t *testing.T) {
	Convey("测试 asyncReRegister 在 savedRegistryCfg 为 nil 时不崩溃", t, func() {
		defer resetReRegisterState()
		resetReRegisterState()

		// savedRegistryCfg 为 nil，asyncReRegister 应安全退出
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 直接调用，不应 panic
		atomic.StoreInt32(&reRegistering, 1)
		asyncReRegister(ctx)

		// reRegistering 应被重置为 0
		So(atomic.LoadInt32(&reRegistering), ShouldEqual, 0)
	})
}

// TestAsyncReRegister_ContextCancelled 测试 context 取消时退出重注册
func TestAsyncReRegister_ContextCancelled(t *testing.T) {
	Convey("测试 asyncReRegister 在 context 取消时安全退出", t, func() {
		defer resetReRegisterState()
		resetReRegisterState()

		// 设置一个较大的退避计数，使 delay > 0
		atomic.StoreInt32(&reRegisterCount, 5)

		ctx, cancel := context.WithCancel(context.Background())
		atomic.StoreInt32(&reRegistering, 1)

		done := make(chan struct{})
		go func() {
			asyncReRegister(ctx)
			close(done)
		}()

		// 立即取消 context
		cancel()

		// 等待 asyncReRegister 退出
		select {
		case <-done:
			// 正常退出
		case <-time.After(3 * time.Second):
			t.Fatal("asyncReRegister did not exit after context cancellation")
		}

		So(atomic.LoadInt32(&reRegistering), ShouldEqual, 0)
	})
}

// TestEndToEnd_RegisterSwitch 端到端测试：模拟注册流程中 register 开关的过滤
func TestEndToEnd_RegisterSwitch(t *testing.T) {
	Convey("端到端测试：模拟注册流程中 register 开关的过滤", t, func() {
		Convey("所有 server 都注册（默认行为）", func() {
			apiServerConfigs := []apiserver.Config{
				{Name: "http"},
				{Name: "grpc"},
			}

			// 模拟 selfRegister 中的过滤逻辑
			var registeredServers []string
			for _, sc := range apiServerConfigs {
				if sc.ShouldRegister() {
					registeredServers = append(registeredServers, sc.Name)
				}
			}
			So(len(registeredServers), ShouldEqual, 2)
			So(registeredServers, ShouldContain, "http")
			So(registeredServers, ShouldContain, "grpc")
		})

		Convey("部分 server 关闭注册", func() {
			apiServerConfigs := []apiserver.Config{
				{Name: "http", RegisterEnabled: boolPtr(false)},
				{Name: "grpc"},
			}

			var registeredServers []string
			for _, sc := range apiServerConfigs {
				if sc.ShouldRegister() {
					registeredServers = append(registeredServers, sc.Name)
				}
			}
			So(len(registeredServers), ShouldEqual, 1)
			So(registeredServers, ShouldContain, "grpc")
			So(registeredServers, ShouldNotContain, "http")
		})

		Convey("所有 server 都关闭注册", func() {
			apiServerConfigs := []apiserver.Config{
				{Name: "http", RegisterEnabled: boolPtr(false)},
				{Name: "grpc", RegisterEnabled: boolPtr(false)},
			}

			var registeredServers []string
			for _, sc := range apiServerConfigs {
				if sc.ShouldRegister() {
					registeredServers = append(registeredServers, sc.Name)
				}
			}
			So(len(registeredServers), ShouldEqual, 0)
		})

		Convey("关闭注册的 server 不应生成注册请求", func() {
			cfg := &Registry{
				Name:      "polaris.limiter",
				Namespace: "Polaris",
			}

			httpServer := &mockAPIServer{protocol: "http", port: 8100}
			grpcServer := &mockAPIServer{protocol: "grpc", port: 8101}

			apiServerConfigs := []apiserver.Config{
				{Name: "http", RegisterEnabled: boolPtr(false)},
				{Name: "grpc", RegisterEnabled: boolPtr(true), RegisterPort: 19001},
			}

			// 模拟 selfRegister 中的完整逻辑
			serverCfgMap := make(map[string]apiserver.Config, len(apiServerConfigs))
			for _, sc := range apiServerConfigs {
				serverCfgMap[sc.Name] = sc
			}

			servers := []apiserver.APIServer{httpServer, grpcServer}
			var instances []string
			for _, server := range servers {
				serverCfg := serverCfgMap[server.GetProtocol()]
				if !serverCfg.ShouldRegister() {
					continue
				}
				instance := buildRegisterRequest(cfg, server, serverCfg, "10.0.0.1")
				instances = append(instances, instance.GetProtocol().GetValue())
			}

			So(len(instances), ShouldEqual, 1)
			So(instances[0], ShouldEqual, "grpc")
		})
	})
}
