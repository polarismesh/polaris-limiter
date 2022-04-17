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
	"fmt"
	"os"

	"github.com/polarismesh/polaris-limit/apiserver"
	"github.com/polarismesh/polaris-limit/pkg/config"
	"github.com/polarismesh/polaris-limit/pkg/log"
	"github.com/polarismesh/polaris-limit/plugin"
	yaml "gopkg.in/yaml.v2"
)

// 配置类
type Config struct {
	Logger     log.Options
	Registry   Registry           `yaml:"registry"`
	APIServers []apiserver.Config `yaml:"api-servers"`
	Limit      config.Config      `yaml:"limit"`
	Plugin     plugin.Config      `yaml:"plugin"`
}

// 自注册配置
type Registry struct {
	Enable               bool   `yaml:"enable"`
	PolarisServerAddress string `yaml:"polaris-server-address"`
	Name                 string `yaml:"name"`
	Namespace            string `yaml:"namespace"`
	Token                string `yaml:"token"`
	HealthCheckEnable    bool   `yaml:"health-check-enable"`
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

	return &config
}
