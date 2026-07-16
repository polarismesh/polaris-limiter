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

package prometheus

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/polarismesh/polaris-limiter/plugin"
)

// TestDumpSampleMetrics 演示真实的 /metrics 文本格式，便于 review 与对接 monitor。
// 用法：go test -run TestDumpSampleMetrics -v ./plugin/statis/prometheus/...
func TestDumpSampleMetrics(t *testing.T) {
	w := NewStaticsWorker()

	// 6 个独立维度，每个 collector 用不同的 CounterKey 避免 stat key 撞键
	c := w.CreateRateLimitStatCollectorV2()
	type sample struct {
		ns, svc, method, appid, uin, labels string
		duration                            time.Duration
		passed, limited                     int64
		counterKey                          uint32
	}
	for _, s := range []sample{
		{"default", "svc-a", "Acquire", "appA", "uin1", "tag=user", time.Second, 80, 20, 1},
		{"default", "svc-a", "Init", "appA", "uin1", "tag=user", time.Second, 23, 0, 2},
		{"default", "svc-b", "Acquire", "appB", "uin2", "tag=order", 5 * time.Second, 5, 2, 3},
		{"prod", "order-svc", "Acquire", "appOrder", "uin3", "region=sh", time.Minute, 1234, 56, 4},
		{"prod", "order-svc", "Init", "appOrder", "uin3", "region=sh", time.Minute, 12, 0, 5},
		{"prod", "payment-svc", "Acquire", "appPay", "uin4", "", 30 * time.Second, 999, 1, 6},
	} {
		v := plugin.PoolGetRateLimitStatValueV2()
		v.Namespace = s.ns
		v.Service = s.svc
		v.Method = s.method
		v.AppId = s.appid
		v.Uin = s.uin
		v.Labels = s.labels
		v.Duration = s.duration
		v.StatKey = plugin.RateLimitStatKeyV2{
			RateLimitStatCounterKeyV2: plugin.RateLimitStatCounterKeyV2{CounterKey: s.counterKey},
		}
		v.GetCurveData().AddPassed(s.passed)
		v.GetCurveData().AddLimited(s.limited)
		c.AddStatValueV2(v)
	}

	// 模拟 process latency
	w.AddProcessTime(20)
	w.AddProcessTime(50)
	w.AddProcessTime(80)
	w.AddProcessTime(999)

	// 模拟实例级 server stats
	plugin.SetServerStatsProvider(func() (int, int) { return 7, 33 })
	defer plugin.SetServerStatsProvider(nil)

	w.flushOnce()

	rec := httptest.NewRecorder()
	promhttp.HandlerFor(w.Registry(), promhttp.HandlerOpts{}).ServeHTTP(
		rec, httptest.NewRequest("GET", "/metrics", nil))

	t.Logf("=== /metrics output BEGIN ===\n%s=== /metrics output END ===", rec.Body.String())
}
