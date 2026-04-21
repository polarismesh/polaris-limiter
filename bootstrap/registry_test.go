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
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	. "github.com/smartystreets/goconvey/convey"
	"gopkg.in/yaml.v2"

	"github.com/polarismesh/polaris-limiter/apiserver"
	polaris "github.com/polarismesh/polaris-limiter/pkg/api/polaris/v1"
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

// resetRegistrar 重置 registrar 单例及 registerFn 注入点，供测试隔离使用。
func resetRegistrar() {
	reg = &registrar{}
	registerFn = func(r *registrar, cfg *Registry, servers []apiserver.APIServer,
		apiServerConfigs []apiserver.Config, serverAddress string) error {
		return r.doSelfRegister(cfg, servers, apiServerConfigs, serverAddress)
	}
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
			So(delay, ShouldBeGreaterThan, time.Duration(0))
		})

		Convey("math.MaxInt32 级 count 不应产生负数或溢出值", func() {
			// 覆盖长期重试场景：避免 math.Pow 结果 +Inf -> int64 溢出为负数
			for _, c := range []int32{11, 20, 100, 1000, 1 << 20, 1<<31 - 1} {
				delay := calcReRegisterDelay(c)
				So(delay, ShouldEqual, maxReRegisterDelay)
			}
		})
	})
}

// TestAsyncReRegister_NilConfig 测试 registryCtx 为 nil 时的防护
func TestAsyncReRegister_NilConfig(t *testing.T) {
	Convey("测试 asyncReRegister 在 registryCtx 为 nil 时不崩溃", t, func() {
		defer resetRegistrar()
		resetRegistrar()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 手动占用 reRegistering 标志，模拟 triggerAsyncReRegister 刚 CAS 成功
		reg.reRegistering.Store(1)
		reg.asyncReRegister(ctx)

		// reRegistering 应被 defer 重置为 0
		So(reg.reRegistering.Load(), ShouldEqual, int32(0))
	})
}

// TestAsyncReRegister_ContextCancelled 测试 context 取消时退出重注册
func TestAsyncReRegister_ContextCancelled(t *testing.T) {
	Convey("测试 asyncReRegister 在 context 取消时安全退出", t, func() {
		defer resetRegistrar()
		resetRegistrar()

		// 预置 registryCtx，避免首轮因 nil 直接返回
		reg.ctx.Store(&registryCtx{cfg: &Registry{Name: "polaris.limiter"}})
		// 设置一个较大的退避计数，使 delay > 0 以便测试 ctx 取消路径
		reg.reRegisterCount.Store(5)

		ctx, cancel := context.WithCancel(context.Background())
		reg.reRegistering.Store(1)

		done := make(chan struct{})
		go func() {
			reg.asyncReRegister(ctx)
			close(done)
		}()

		// 立即取消 context
		cancel()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("asyncReRegister did not exit after context cancellation")
		}

		So(reg.reRegistering.Load(), ShouldEqual, int32(0))
	})
}

// TestTriggerAsyncReRegister_CASDedup 测试连续触发只有一个 goroutine 执行重注册
func TestTriggerAsyncReRegister_CASDedup(t *testing.T) {
	Convey("连续触发 triggerAsyncReRegister 时，CAS 保证仅一次执行", t, func() {
		defer resetRegistrar()
		resetRegistrar()

		reg.ctx.Store(&registryCtx{cfg: &Registry{Name: "polaris.limiter"}})

		var calls atomic.Int32
		release := make(chan struct{})
		registerFn = func(r *registrar, cfg *Registry, servers []apiserver.APIServer,
			apiServerConfigs []apiserver.Config, serverAddress string) error {
			calls.Add(1)
			<-release
			return nil
		}

		reg.triggerAsyncReRegister(context.Background())
		// 第二次触发应被 CAS 拦截
		reg.triggerAsyncReRegister(context.Background())
		reg.triggerAsyncReRegister(context.Background())

		// 等待首个 goroutine 进入 registerFn
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		So(calls.Load(), ShouldEqual, int32(1))
		So(reg.reRegistering.Load(), ShouldEqual, int32(1))

		close(release)
		// 等待重注册协程退出
		deadline = time.Now().Add(2 * time.Second)
		for reg.reRegistering.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		So(reg.reRegistering.Load(), ShouldEqual, int32(0))
		So(calls.Load(), ShouldEqual, int32(1))
	})
}

// TestAsyncReRegister_SuccessResetsCounters 测试重注册成功后重置计数器
func TestAsyncReRegister_SuccessResetsCounters(t *testing.T) {
	Convey("重注册成功后应重置 notFoundCount 与 reRegisterCount", t, func() {
		defer resetRegistrar()
		resetRegistrar()

		reg.ctx.Store(&registryCtx{cfg: &Registry{Name: "polaris.limiter"}})
		reg.notFoundCount.Store(5)
		reg.reRegisterCount.Store(0) // 首轮 delay=0 以缩短测试耗时
		reg.reRegistering.Store(1)

		registerFn = func(r *registrar, cfg *Registry, servers []apiserver.APIServer,
			apiServerConfigs []apiserver.Config, serverAddress string) error {
			return nil
		}

		reg.asyncReRegister(context.Background())

		So(reg.notFoundCount.Load(), ShouldEqual, int32(0))
		So(reg.reRegisterCount.Load(), ShouldEqual, int32(0))
		So(reg.reRegistering.Load(), ShouldEqual, int32(0))
	})
}

// TestAsyncReRegister_RetryOnFailure 测试失败后在内部循环重试直到成功
func TestAsyncReRegister_RetryOnFailure(t *testing.T) {
	Convey("重注册失败后应继续内部循环重试，成功后重置计数器", t, func() {
		defer resetRegistrar()
		resetRegistrar()

		reg.ctx.Store(&registryCtx{cfg: &Registry{Name: "polaris.limiter"}})
		reg.reRegistering.Store(1)

		// 第 1 次失败 -> reRegisterCount 变为 1，下轮 delay >= serverTtl（5s）
		// 为了避免测试耗时，通过构造 ctx 提前取消后验证计数器累加行为
		var calls atomic.Int32
		registerFn = func(r *registrar, cfg *Registry, servers []apiserver.APIServer,
			apiServerConfigs []apiserver.Config, serverAddress string) error {
			n := calls.Add(1)
			if n == 1 {
				return fmt.Errorf("mock failure")
			}
			return nil
		}

		// 使用带超时的 ctx，避免因失败退避时间过长而挂起
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		reg.asyncReRegister(ctx)

		// 首轮失败会累加，然后进入 delay（约 5~10s），被 ctx 取消返回。
		// 因此 reRegisterCount 至少为 1；calls 至少为 1 且 ≤ 2。
		So(calls.Load(), ShouldBeGreaterThanOrEqualTo, int32(1))
		So(calls.Load(), ShouldBeLessThanOrEqualTo, int32(2))
		So(reg.reRegisterCount.Load(), ShouldBeGreaterThanOrEqualTo, int32(1))
		So(reg.reRegistering.Load(), ShouldEqual, int32(0))
	})
}

// TestHandleHeartbeatResp 测试心跳响应处理逻辑
func TestHandleHeartbeatResp(t *testing.T) {
	Convey("测试 handleHeartbeatResp 对响应码的处理", t, func() {
		defer resetRegistrar()

		instance := &polaris.Instance{
			Host: &wrappers.StringValue{Value: "10.0.0.1"},
			Port: &wrappers.UInt32Value{Value: 8101},
		}

		Convey("心跳 err 非空时返回 err", func() {
			resetRegistrar()
			err := reg.handleHeartbeatResp(context.Background(), instance, nil, fmt.Errorf("boom"))
			So(err, ShouldNotBeNil)
			So(reg.notFoundCount.Load(), ShouldEqual, int32(0))
		})

		Convey("成功响应应清零 notFoundCount", func() {
			resetRegistrar()
			reg.notFoundCount.Store(3)
			resp := &polaris.Response{Code: &wrappers.UInt32Value{Value: 200000}}
			err := reg.handleHeartbeatResp(context.Background(), instance, resp, nil)
			So(err, ShouldBeNil)
			So(reg.notFoundCount.Load(), ShouldEqual, int32(0))
		})

		Convey("NOT_FOUND 但未超阈值时不触发重注册", func() {
			resetRegistrar()
			// 阻塞 registerFn 以便观测是否被触发
			registerFn = func(r *registrar, cfg *Registry, servers []apiserver.APIServer,
				apiServerConfigs []apiserver.Config, serverAddress string) error {
				return nil
			}
			resp := &polaris.Response{Code: &wrappers.UInt32Value{Value: notFoundResourceCode}}
			err := reg.handleHeartbeatResp(context.Background(), instance, resp, nil)
			So(err, ShouldNotBeNil)
			So(reg.notFoundCount.Load(), ShouldEqual, int32(1))
			So(reg.reRegistering.Load(), ShouldEqual, int32(0))
		})

		Convey("NOT_FOUND 且超阈值时触发异步重注册", func() {
			resetRegistrar()
			reg.ctx.Store(&registryCtx{cfg: &Registry{Name: "polaris.limiter"}})
			// 预置计数让本次累加后刚好超阈值
			reg.notFoundCount.Store(int32(maxHeartbeatFailCount))

			release := make(chan struct{})
			var called atomic.Int32
			registerFn = func(r *registrar, cfg *Registry, servers []apiserver.APIServer,
				apiServerConfigs []apiserver.Config, serverAddress string) error {
				called.Add(1)
				<-release
				return nil
			}

			resp := &polaris.Response{Code: &wrappers.UInt32Value{Value: notFoundResourceCode}}
			err := reg.handleHeartbeatResp(context.Background(), instance, resp, nil)
			So(err, ShouldNotBeNil)

			// 等待 goroutine 启动
			deadline := time.Now().Add(2 * time.Second)
			for called.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			So(reg.reRegistering.Load(), ShouldEqual, int32(1))
			So(called.Load(), ShouldEqual, int32(1))

			close(release)
			// 等待清理
			deadline = time.Now().Add(2 * time.Second)
			for reg.reRegistering.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			So(reg.reRegistering.Load(), ShouldEqual, int32(0))
		})
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
