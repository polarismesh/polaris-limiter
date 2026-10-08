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

import (
	"context"
	"sync/atomic"
)

const (
	// StatusUp 组件/整体健康。
	StatusUp = "UP"
	// StatusDown 组件/整体不健康。
	StatusDown = "DOWN"
)

// Component 单个健康检查分组。
type Component struct {
	Status  string         `json:"status"`
	Details map[string]any `json:"details,omitempty"`
}

// Report /liveness 与 /readiness 的聚合响应。
type Report struct {
	Status     string               `json:"status"`
	Components map[string]Component `json:"components"`
}

// Checker 聚合 basic 与 ioHang 分组，供 HTTP 与心跳复用。
type Checker struct {
	ioHangEnabled   bool
	affectHeartbeat bool
	detector        *IOHangDetector
}

var defaultChecker atomic.Pointer[Checker]

// SetDefault 由 bootstrap 在启动期写入；测试可替换。
func SetDefault(c *Checker) {
	defaultChecker.Store(c)
}

// Default 返回当前 Checker，可能为 nil。
func Default() *Checker {
	return defaultChecker.Load()
}

// NewChecker 构造聚合检查器。detector 仅在 ioHang 启用时非 nil。
func NewChecker(cfg Config, detector *IOHangDetector) *Checker {
	return &Checker{
		ioHangEnabled:   cfg.Enabled(),
		affectHeartbeat: cfg.HeartbeatAffected(),
		detector:        detector,
	}
}

// Liveness 只反映进程可接受 HTTP，不含任何 IO。
func (c *Checker) Liveness() Report {
	return Report{
		Status: StatusUp,
		Components: map[string]Component{
			"basic": {Status: StatusUp},
		},
	}
}

// Readiness 在 liveness 基础上叠加 IOHang；未启用时仅 basic。
func (c *Checker) Readiness() Report {
	if c == nil || !c.ioHangEnabled || c.detector == nil {
		return (&Checker{}).Liveness()
	}
	report := c.Liveness()
	up, details := c.detector.Status()
	comp := Component{Status: StatusUp, Details: details}
	if !up {
		comp.Status = StatusDown
		report.Status = StatusDown
	}
	report.Components["ioHang"] = comp
	return report
}

// AllowHeartbeat 为 false 时心跳链路应跳过上报。判定路径无 IO。
func (c *Checker) AllowHeartbeat() bool {
	if c == nil || !c.ioHangEnabled || !c.affectHeartbeat || c.detector == nil {
		return true
	}
	return c.detector.Healthy()
}

// WaitFirstResult 供启动期注册门禁调用：等首次探测出结论后再用 AllowHeartbeat 决定是否注册。
// 北极星联动未开启时立即返回。
func (c *Checker) WaitFirstResult(ctx context.Context) {
	if c == nil || !c.ioHangEnabled || !c.affectHeartbeat || c.detector == nil {
		return
	}
	c.detector.WaitFirstResult(ctx)
}

// LivenessReport 供 HTTP handler 调用，Checker 未注入时仍返回 basic UP。
func LivenessReport() Report {
	if c := Default(); c != nil {
		return c.Liveness()
	}
	return (&Checker{}).Liveness()
}

// ReadinessReport 供 HTTP handler 调用。
func ReadinessReport() Report {
	if c := Default(); c != nil {
		return c.Readiness()
	}
	return (&Checker{}).Readiness()
}

// AllowHeartbeat 供心跳与注册链路调用。
func AllowHeartbeat() bool {
	if c := Default(); c != nil {
		return c.AllowHeartbeat()
	}
	return true
}

// WaitFirstResult 供启动期注册门禁调用。
func WaitFirstResult(ctx context.Context) {
	Default().WaitFirstResult(ctx)
}
