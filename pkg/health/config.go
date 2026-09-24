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

package health

import "time"

const (
	defaultInterval           = 5 * time.Second
	defaultTimeout            = 3 * time.Second
	defaultStaleness          = 15 * time.Second
	defaultProbePath          = "log/polaris-limiter-probe.log"
	defaultRotationMaxSize    = 10
	defaultRotationMaxAge     = 7
	defaultRotationMaxBackups = 3
	defaultEnable             = true
	defaultAffectHeartbeat    = true
	probeLine                 = "iohang-probe\n"
)

// Config IOHang 检测配置。字段均为可选，零值/缺省由 WithDefaults 填充。
type Config struct {
	// Enable 是否启用 IOHang 检测；不填默认为 true
	Enable *bool `yaml:"enable"`
	// Interval 后台检测周期（定时写探测文件，不由 /readiness 触发）
	Interval time.Duration `yaml:"interval"`
	// Timeout 单次写超时阈值，超过即判 DOWN
	Timeout time.Duration `yaml:"timeout"`
	// Staleness 距上次探测完成超过此值判 DOWN；小于 2*Interval+Timeout 时自动抬到该值
	Staleness time.Duration `yaml:"staleness"`
	// AffectHeartbeat DOWN 时是否跳过北极星心跳上报；不填默认为 true
	AffectHeartbeat *bool `yaml:"affect-heartbeat"`
	// Path 独立探测文件路径，须与业务日志同目录以保证同盘
	Path string `yaml:"path"`
	// RotationMaxSize 探测文件滚动大小（MB）
	RotationMaxSize int `yaml:"rotation-max-size"`
	// RotationMaxAge 探测文件保留天数
	RotationMaxAge int `yaml:"rotation-max-age"`
	// RotationMaxBackups 探测文件备份个数
	RotationMaxBackups int `yaml:"rotation-max-backups"`
}

// WithDefaults 返回填好缺省值的副本，不修改接收者。
func (c Config) WithDefaults() Config {
	out := c
	if out.Interval <= 0 {
		out.Interval = defaultInterval
	}
	if out.Timeout <= 0 {
		out.Timeout = defaultTimeout
	}
	if out.Staleness <= 0 {
		out.Staleness = defaultStaleness
	}
	// staleness 过小会在两次正常探测之间误判 DOWN，导致心跳抖动
	if minStaleness := 2*out.Interval + out.Timeout; out.Staleness < minStaleness {
		out.Staleness = minStaleness
	}
	if out.Path == "" {
		out.Path = defaultProbePath
	}
	if out.RotationMaxSize <= 0 {
		out.RotationMaxSize = defaultRotationMaxSize
	}
	if out.RotationMaxAge <= 0 {
		out.RotationMaxAge = defaultRotationMaxAge
	}
	if out.RotationMaxBackups <= 0 {
		out.RotationMaxBackups = defaultRotationMaxBackups
	}
	return out
}

// Enabled 是否启用检测；未配置时默认 true。
func (c Config) Enabled() bool {
	if c.Enable == nil {
		return defaultEnable
	}
	return *c.Enable
}

// HeartbeatAffected DOWN 时是否跳过心跳；未配置时默认 true。
func (c Config) HeartbeatAffected() bool {
	if c.AffectHeartbeat == nil {
		return defaultAffectHeartbeat
	}
	return *c.AffectHeartbeat
}
