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
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/polarismesh/polaris-limiter/pkg/log"
)

// firstResultPoll 是 WaitFirstResult 轮询 inflight 超时的间隔。
const firstResultPoll = 20 * time.Millisecond

// deviceErrnos 表示存储设备本身故障（NFS / virtio / 云盘掉盘常见为快速返回 EIO 而非阻塞）。
// 与磁盘满、只读、无权限不同，这类故障局限在单个节点，换副本即可绕开，应当摘流。
var deviceErrnos = []syscall.Errno{syscall.EIO, syscall.ENXIO, syscall.ENODEV}

// isDeviceError 判断探测写入错误是否来自存储设备故障。
// lumberjack 打开 / 滚动文件失败时以 %s 拼接原错误，错误链断开，需再按 errno 文本兜底。
func isDeviceError(err error) bool {
	for _, errno := range deviceErrnos {
		if errors.Is(err, errno) || strings.Contains(err.Error(), errno.Error()) {
			return true
		}
	}
	return false
}

// probeError 是最近一次探测失败的记录；device 为 true 时判 DOWN。
type probeError struct {
	msg    string
	device bool
}

// IOHangDetector 后台周期性执行一次同步落盘写，用「当前 inflight 时长」、
// 「上次探测完成时间」与「设备错误」三个维度判定 IOHang。
// 探测写本身可能永久阻塞，因此绝不能在判定路径上等待它完成。
// 写入立即返回的环境类错误（磁盘满、只读、路径不可写）不判 DOWN，只记录在 details 中，
// 否则全量副本同时磁盘满时会把限流服务整体摘空；EIO 等设备错误判 DOWN，直到下一次探测成功。
type IOHangDetector struct {
	interval  time.Duration
	timeout   time.Duration
	staleness time.Duration
	sink      Writer

	probing       atomic.Int32 // 0/1，保证最多 1 个探测 goroutine
	inflightSince atomic.Int64 // 当前探测开始的 UnixNano；0 表示空闲
	lastDoneAt    atomic.Int64 // 上次探测完成（成功或失败）的 UnixNano
	lastSuccessAt atomic.Int64 // 上次探测成功的 UnixNano
	lastCostNs    atomic.Int64
	lastErr       atomic.Pointer[probeError] // nil 表示上次探测成功

	firstDoneOnce sync.Once
	firstDone     chan struct{} // 首次探测完成后关闭
}

// NewIOHangDetector 构造检测器。cfg 应已经 WithDefaults。
func NewIOHangDetector(cfg Config, sink Writer) *IOHangDetector {
	return &IOHangDetector{
		interval:  cfg.Interval,
		timeout:   cfg.Timeout,
		staleness: cfg.Staleness,
		sink:      sink,
		firstDone: make(chan struct{}),
	}
}

// WaitFirstResult 阻塞到首次探测出结论：探测已完成，或 inflight 超过 timeout 判 DOWN。
// 返回后 Status 不再处于 warming-up。最多等待 2*timeout，防止检测器未 Start 时永久阻塞。
func (d *IOHangDetector) WaitFirstResult(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*d.timeout)
	defer cancel()
	ticker := time.NewTicker(firstResultPoll)
	defer ticker.Stop()
	for {
		select {
		case <-d.firstDone:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !d.Healthy() {
				return
			}
		}
	}
}

// Start 启动后台检测循环。启动时立刻做第一次探测。
func (d *IOHangDetector) Start(ctx context.Context) {
	d.kickProbe()
	go d.loop(ctx)
}

func (d *IOHangDetector) loop(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.kickProbe()
		}
	}
}

func (d *IOHangDetector) kickProbe() {
	if !d.probing.CompareAndSwap(0, 1) {
		return
	}
	go func() {
		defer d.probing.Store(0)
		hadErr := d.lastErr.Load() != nil
		err := d.ProbeOnce()
		d.logTransition(hadErr, err)
	}()
}

// logTransition 只在探测 goroutine 返回后调用：此时磁盘已不再阻塞该写路径。
func (d *IOHangDetector) logTransition(hadErr bool, err error) {
	cost := time.Duration(d.lastCostNs.Load())
	switch {
	case err != nil && !hadErr && isDeviceError(err):
		log.Errorf("[Health] iohang probe hit device error, readiness DOWN: %v", err)
	case err != nil && !hadErr:
		log.Errorf("[Health] iohang probe write failed: %v", err)
	case err == nil && hadErr:
		log.Infof("[Health] iohang probe write recovered, cost %s", cost)
	}
	if cost > d.timeout {
		log.Warnf("[Health] iohang probe took %s, exceeds timeout %s", cost, d.timeout)
	}
}

// ProbeOnce 对探测文件执行一次 Write + Sync。测试与 kickProbe 共用。
func (d *IOHangDetector) ProbeOnce() error {
	start := time.Now()
	d.inflightSince.Store(start.UnixNano())
	defer d.inflightSince.Store(0)

	err := d.sink.WriteAndSync([]byte(probeLine))
	done := time.Now()
	d.lastCostNs.Store(int64(done.Sub(start)))
	d.lastDoneAt.Store(done.UnixNano())
	defer d.firstDoneOnce.Do(func() { close(d.firstDone) })
	if err != nil {
		d.lastErr.Store(&probeError{msg: err.Error(), device: isDeviceError(err)})
		return err
	}
	d.lastErr.Store(nil)
	d.lastSuccessAt.Store(done.UnixNano())
	return nil
}

// Healthy 等价于 Status 的 up 位，无阻塞、无 IO。
func (d *IOHangDetector) Healthy() bool {
	up, _ := d.Status()
	return up
}

// Status 供 /readiness 与心跳链路调用，无阻塞、无 IO。
func (d *IOHangDetector) Status() (up bool, details map[string]any) {
	now := time.Now()
	details = make(map[string]any, 4)
	lastErr := d.lastErr.Load()
	if lastErr != nil {
		details["lastError"] = lastErr.msg
	}

	if inflight := d.inflightSince.Load(); inflight != 0 {
		dur := now.Sub(time.Unix(0, inflight))
		if dur > d.timeout {
			details["reason"] = fmt.Sprintf("write inflight for %s, exceeds timeout %s",
				dur.Round(time.Millisecond), d.timeout)
			return false, details
		}
	}
	lastDone := d.lastDoneAt.Load()
	if lastDone == 0 {
		details["reason"] = "warming-up"
		return true, details
	}
	if since := now.Sub(time.Unix(0, lastDone)); since > d.staleness {
		details["reason"] = fmt.Sprintf("last probe done %s ago, exceeds staleness %s",
			since.Round(time.Millisecond), d.staleness)
		return false, details
	}
	if lastErr != nil && lastErr.device {
		details["reason"] = "device error: " + lastErr.msg
		return false, details
	}
	if last := d.lastSuccessAt.Load(); last != 0 {
		details["lastSuccessMs"] = float64(d.lastCostNs.Load()) / float64(time.Millisecond)
		details["lastSuccessAt"] = time.Unix(0, last).In(time.Local).Format(time.RFC3339)
	}
	return true, details
}
