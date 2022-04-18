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

package plugin

import (
	"fmt"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"sync"
)

var (
	pluginSet     = make(map[string]Plugin)
	config        = &Config{}
	initializeSet = make(map[string]Plugin)
)

// 通用插件接口
type Plugin interface {
	Name() string
	Initialize(c *ConfigEntry) error
	Destroy() error
}

// 插件总配置
type Config struct {
	Storage *ConfigEntry `yaml:"storage"`
	Statis  *ConfigEntry `yaml:"statis"`
}

// 每个插件配置
type ConfigEntry struct {
	Name   string                 `yaml:"name"`
	Option map[string]interface{} `yaml:"option"`
}

// 注册插件
func RegisterPlugin(name string, plugin Plugin) {
	if _, exist := pluginSet[name]; exist {
		panic(fmt.Sprintf("existed plugin: name=%v", name))
	}

	pluginSet[name] = plugin
}

// 插件全局配置
func GlobalInitialize(c *Config) {
	config = c
}

// 插件全局销毁函数
func GlobalDestroy() {
	for _, plugin := range initializeSet {
		_ = plugin.Destroy()
	}
}

// 插件初始化全局函数
func subInitialize(name string, cfg *ConfigEntry, once *sync.Once) (Plugin, error) {
	if cfg == nil {
		log.Warnf("plugin config for %s is emptry", name)
		return nil, nil
	}
	plugin, ok := pluginSet[cfg.Name]
	if !ok {
		return nil, nil
	}

	var err error
	once.Do(func() {
		err = plugin.Initialize(cfg)
		initializeSet[cfg.Name] = plugin
	})
	return plugin, err
}
