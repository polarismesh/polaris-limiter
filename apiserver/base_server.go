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

package apiserver

import "errors"

// Config API服务器配置 配置文件
type Config struct {
	Name            string                 `yaml:"name"`
	Option          map[string]interface{} `yaml:"option"`
	RegisterEnabled *bool                  `yaml:"register-enabled"` // 是否注册到注册中心，不填或为 true 则注册，设为 false 则不注册。注意：请使用 ShouldRegister() 方法判断，不要直接读取此字段
	RegisterPort    uint32                 `yaml:"register-port"`    // 自定义注册端口，不填或为0则使用 server 实际监听端口
}

// ShouldRegister 返回该 API Server 是否需要注册到注册中心
// 当 Register 字段未配置（nil）或为 true 时返回 true
func (c Config) ShouldRegister() bool {
	return c.RegisterEnabled == nil || *c.RegisterEnabled
}

// APIServer API服务器接口
type APIServer interface {
	Initialize(option map[string]interface{}) error
	Run(errCh chan error)
	Stop()
	GetProtocol() string
	GetPort() uint32
}

var (
	Slots = make(map[string]APIServer)
)

// Register 注册API服务器
func Register(name string, server APIServer) error {
	if _, exist := Slots[name]; exist {
		err := errors.New("api server name exist")
		return err
	}

	Slots[name] = server

	return nil
}
