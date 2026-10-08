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
	"fmt"
	"os"

	"gopkg.in/yaml.v2"

	"github.com/polarismesh/polaris-limiter/apiserver"
	"github.com/polarismesh/polaris-limiter/pkg/config"
	"github.com/polarismesh/polaris-limiter/pkg/health"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/plugin"
)

// Config 配置类
type Config struct {
	Logger     log.Options
	Registry   Registry           `yaml:"registry"`
	APIServers []apiserver.Config `yaml:"api-servers"`
	Limit      config.Config      `yaml:"limit"`
	Plugin     plugin.Config      `yaml:"plugin"`
	Health     HealthConfig       `yaml:"health"`
}

// HealthConfig 健康检查配置
type HealthConfig struct {
	// IOHang IOHang 检测；不填时全部走默认值
	IOHang health.Config `yaml:"iohang"`
}

// Registry 自注册配置
type Registry struct {
	Enable               bool   `yaml:"enable"`
	PolarisServerAddress string `yaml:"polaris-server-address"`
	Name                 string `yaml:"name"`
	Namespace            string `yaml:"namespace"`
	Host                 string `yaml:"host"`
	Token                string `yaml:"token"`
	HealthCheckEnable    bool   `yaml:"health-check-enable"`
	// Region 实例所属地域（如 huanan），供 SDK 跨地域就近路由使用；为空则不下发该层级
	Region string `yaml:"region"`
	// Zone 实例所属可用区（如 ap-guangzhou），供 SDK 跨地域就近路由使用；为空则不下发该层级
	Zone string `yaml:"zone"`
	// Campus 实例所属园区（如 ap-guangzhou-1），供 SDK 跨地域就近路由使用；为空则不下发该层级
	Campus string `yaml:"campus"`
}

// 解析配置文件
func loadConfig(configPath string) *Config {
	if configPath == "" {
		bootExit("config path is empty")
	}

	file, err := os.Open(configPath)
	if err != nil {
		bootExit(fmt.Sprintf("os open config path(%s) err: %s", configPath, err.Error()))
	}
	defer file.Close()

	var config Config
	if err := yaml.NewDecoder(file).Decode(&config); err != nil {
		bootExit(fmt.Sprintf("decode config err: %s", err.Error()))
	}
	// 在打印配置前补齐默认值，使 load config 输出即为实际生效值
	config.Health.IOHang = config.Health.IOHang.WithDefaults()

	return &config
}
