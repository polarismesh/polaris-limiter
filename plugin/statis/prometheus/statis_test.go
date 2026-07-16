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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/polarismesh/polaris-limiter/plugin"
)

// 注意：每个测试用例使用独立的 worker，避免共享 prometheus.Registry 导致指标污染。

func TestStaticsWorker_AddProcessTime(t *testing.T) {
	Convey("AddProcessTime 累积与重置", t, func() {
		w := NewStaticsWorker()

		Convey("初始状态 reset 应该返回零", func() {
			total, max, count := w.resetProcessStats()
			So(total, ShouldEqual, int64(0))
			So(max, ShouldEqual, int64(0))
			So(count, ShouldEqual, int64(0))
		})

		Convey("多次 AddProcessTime 后能累积 total / max / count", func() {
			w.AddProcessTime(100)
			w.AddProcessTime(50)
			w.AddProcessTime(300)
			total, max, count := w.resetProcessStats()
			So(total, ShouldEqual, int64(450))
			So(max, ShouldEqual, int64(300))
			So(count, ShouldEqual, int64(3))
		})

		Convey("reset 后再次读取应回到零", func() {
			w.AddProcessTime(10)
			_, _, _ = w.resetProcessStats()
			total, max, count := w.resetProcessStats()
			So(total, ShouldEqual, int64(0))
			So(max, ShouldEqual, int64(0))
			So(count, ShouldEqual, int64(0))
		})

		Convey("负值会被丢弃", func() {
			w.AddProcessTime(-10)
			_, _, count := w.resetProcessStats()
			So(count, ShouldEqual, int64(0))
		})
	})
}

func TestStaticsWorker_CollectorLifecycle(t *testing.T) {
	Convey("Collector 注册 / drop 流程", t, func() {
		w := NewStaticsWorker()

		Convey("初始化后没有活跃 collector", func() {
			So(len(w.snapshotCollectors()), ShouldEqual, 0)
		})

		Convey("CreateRateLimitStatCollectorV2 后应被纳入 active", func() {
			c := w.CreateRateLimitStatCollectorV2()
			So(c, ShouldNotBeNil)
			So(len(w.snapshotCollectors()), ShouldEqual, 1)

			Convey("Drop 后从 active 移除并进入 dropped", func() {
				w.DropRateLimitStatCollector(c)
				So(len(w.snapshotCollectors()), ShouldEqual, 0)

				dropped := w.drainDropped()
				So(len(dropped), ShouldEqual, 1)

				// drain 之后应被清空
				dropped2 := w.drainDropped()
				So(len(dropped2), ShouldEqual, 0)
			})
		})

		Convey("CreateRateLimitStatCollectorV1 也能正常注册和归还", func() {
			c := w.CreateRateLimitStatCollectorV1()
			So(c, ShouldNotBeNil)
			So(len(w.snapshotCollectors()), ShouldEqual, 1)
			w.DropRateLimitStatCollector(c)
			So(len(w.snapshotCollectors()), ShouldEqual, 0)
		})

		Convey("Drop nil 不会 panic", func() {
			So(func() { w.DropRateLimitStatCollector(nil) }, ShouldNotPanic)
		})
	})
}

func TestStaticsWorker_FlushOnce(t *testing.T) {
	Convey("flushOnce 聚合 collector 数据并清零", t, func() {
		w := NewStaticsWorker()
		c := w.CreateRateLimitStatCollectorV2()

		// 注入 V2 stat value，包含完整 7 维度（namespace/service/method/appid/uin/labels/duration）
		v := plugin.PoolGetRateLimitStatValueV2()
		v.Namespace = "ns1"
		v.Service = "svc1"
		v.Method = "m1"
		v.AppId = "app1"
		v.Uin = "uin1"
		v.Labels = "k=v"
		v.Duration = time.Second
		v.StatKey = plugin.RateLimitStatKeyV2{
			RateLimitStatCounterKeyV2: plugin.RateLimitStatCounterKeyV2{CounterKey: 1},
		}
		v.GetCurveData().AddPassed(7)
		v.GetCurveData().AddLimited(3)
		c.AddStatValueV2(v)

		// prometheus 输出 label 按字母序：appid,duration,labels,method,namespace,service,uin
		expectLabels := `appid="app1",duration="1s",labels="k=v",method="m1",namespace="ns1",service="svc1",uin="uin1"`

		Convey("第一次 flush 后 Counter 增长", func() {
			w.flushOnce()

			// 通过 promhttp 输出验证
			handler := promhttp.HandlerFor(w.Registry(), promhttp.HandlerOpts{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/metrics", nil)
			handler.ServeHTTP(rec, req)
			body := rec.Body.String()
			So(body, ShouldContainSubstring, `ratelimit_rq_total{`+expectLabels+`} 10`)
			So(body, ShouldContainSubstring, `ratelimit_rq_pass{`+expectLabels+`} 7`)
			So(body, ShouldContainSubstring, `ratelimit_rq_limit{`+expectLabels+`} 3`)

			Convey("第二次 flush 无新增量时 Counter 不再增长", func() {
				w.flushOnce()
				rec2 := httptest.NewRecorder()
				handler.ServeHTTP(rec2, httptest.NewRequest("GET", "/metrics", nil))
				So(rec2.Body.String(), ShouldContainSubstring,
					`ratelimit_rq_total{`+expectLabels+`} 10`)
			})
		})
	})
}

func TestStaticsWorker_FlushProcessAndServerStats(t *testing.T) {
	Convey("flushOnce 写入 process 与实例级 Gauge", t, func() {
		w := NewStaticsWorker()

		// counter_count 仍来自注入的 provider；active_streams 改为取活跃 collector 数（真实 stream 数），
		// 故 provider 的 streams 值（5）应被忽略。
		plugin.SetServerStatsProvider(func() (int, int) { return 5, 12 })
		defer plugin.SetServerStatsProvider(nil)

		// 建立 3 个 collector 模拟 3 条活跃 stream
		w.CreateRateLimitStatCollectorV2()
		w.CreateRateLimitStatCollectorV2()
		w.CreateRateLimitStatCollectorV2()

		w.AddProcessTime(100)
		w.AddProcessTime(300)
		w.flushOnce()

		handler := promhttp.HandlerFor(w.Registry(), promhttp.HandlerOpts{})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		body := rec.Body.String()

		So(body, ShouldContainSubstring, "ratelimit_process_avg_us 200")
		So(body, ShouldContainSubstring, "ratelimit_process_max_us 300")
		// active_streams == 活跃 collector 数（3），而非 provider 的 5
		So(body, ShouldContainSubstring, "ratelimit_active_streams 3")
		So(body, ShouldContainSubstring, "ratelimit_counter_count 12")

		Convey("count=0 时 avg/max 写零", func() {
			w.flushOnce()
			rec2 := httptest.NewRecorder()
			handler.ServeHTTP(rec2, httptest.NewRequest("GET", "/metrics", nil))
			So(rec2.Body.String(), ShouldContainSubstring, "ratelimit_process_avg_us 0")
			So(rec2.Body.String(), ShouldContainSubstring, "ratelimit_process_max_us 0")
		})
	})
}

func TestStaticsWorker_AddProcessTimeRace(t *testing.T) {
	Convey("AddProcessTime / resetProcessStats 并发安全", t, func() {
		w := NewStaticsWorker()
		const writers = 8
		const perWriter = 1000

		var writerWg sync.WaitGroup
		for i := 0; i < writers; i++ {
			writerWg.Add(1)
			go func() {
				defer writerWg.Done()
				for j := 0; j < perWriter; j++ {
					w.AddProcessTime(int64(j%5 + 1))
				}
			}()
		}

		// reader 协程持续读取，直到 stop 被关闭
		stop := make(chan struct{})
		var readerWg sync.WaitGroup
		readerWg.Add(1)
		go func() {
			defer readerWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _, _ = w.resetProcessStats()
				}
			}
		}()

		writerWg.Wait()
		close(stop)
		readerWg.Wait()
		// 不验证具体值，依赖 -race 检测
		So(true, ShouldBeTrue)
	})
}

// injectCurve 向 collector 注入一条带完整 7 维度、指定 passed/limited 的曲线增量。
func injectCurve(c *plugin.RateLimitStatCollectorV2, svc string, passed, limited int64) {
	v := plugin.PoolGetRateLimitStatValueV2()
	v.Namespace = "ns1"
	v.Service = svc
	v.Method = "m1"
	v.AppId = "app1"
	v.Uin = "uin1"
	v.Labels = "k=v"
	v.Duration = time.Second
	v.StatKey = plugin.RateLimitStatKeyV2{
		RateLimitStatCounterKeyV2: plugin.RateLimitStatCounterKeyV2{CounterKey: 1},
	}
	v.GetCurveData().AddPassed(passed)
	v.GetCurveData().AddLimited(limited)
	c.AddStatValueV2(v)
}

// TestStaticsWorker_EvictStaleSeries 验证 series TTL 淘汰：连续 staleFlushCycles 轮无增量后
// 对应维度从 CounterVec 删除，收敛基数。
func TestStaticsWorker_EvictStaleSeries(t *testing.T) {
	Convey("陈旧 series 应在 staleFlushCycles 轮无增量后被淘汰", t, func() {
		w := NewStaticsWorker()
		c := w.CreateRateLimitStatCollectorV2()
		handler := promhttp.HandlerFor(w.Registry(), promhttp.HandlerOpts{})
		dump := func() string {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
			return rec.Body.String()
		}

		// 第 1 轮：有增量，series 建立
		injectCurve(c, "svc1", 7, 3)
		w.flushOnce()
		So(dump(), ShouldContainSubstring, `ratelimit_rq_total{`)
		So(len(w.lastSeen), ShouldEqual, 1)

		// 之后连续无增量：staleFlushCycles-1 轮内 series 仍在（覆盖 monitor delta 连续性）
		for i := 0; i < staleFlushCycles-1; i++ {
			w.flushOnce()
		}
		So(dump(), ShouldContainSubstring, `ratelimit_rq_total{`)
		So(len(w.lastSeen), ShouldEqual, 1)

		// 再 flush 一轮，达到阈值，series 被淘汰
		w.flushOnce()
		So(dump(), ShouldNotContainSubstring, `ratelimit_rq_total{`)
		So(len(w.lastSeen), ShouldEqual, 0)

		Convey("同维度再次产生增量可重新建立 series", func() {
			injectCurve(c, "svc1", 5, 0)
			w.flushOnce()
			So(dump(), ShouldContainSubstring, `ratelimit_rq_total{`)
			So(len(w.lastSeen), ShouldEqual, 1)
		})
	})
}

// 简单确保 init() 注册了插件
func TestStaticsWorker_Registered(t *testing.T) {
	Convey("插件会被注册到全局 plugin set 里", t, func() {
		// 间接验证：通过插件名构造 ConfigEntry，检查 Initialize / Destroy 不报错
		w := NewStaticsWorker()
		err := w.Initialize(&plugin.ConfigEntry{Name: PluginName})
		So(err, ShouldBeNil)
		// 立即 Destroy，避免 flush goroutine 残留
		So(w.Destroy(), ShouldBeNil)
		// 等待 flush 协程退出
		time.Sleep(50 * time.Millisecond)
	})
}

// 验证插件名常量
func TestPluginName(t *testing.T) {
	Convey("插件名为 prometheus", t, func() {
		So(PluginName, ShouldEqual, "prometheus")
		So(strings.ToLower(NewStaticsWorker().Name()), ShouldEqual, "prometheus")
	})
}
