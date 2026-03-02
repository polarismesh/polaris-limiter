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
	"os"
	"testing"

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
