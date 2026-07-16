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

package file

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/polarismesh/polaris-limiter/plugin"
)

// countSyncMap 统计 sync.Map 当前 entry 数（sync.Map 无 Len）。
func countSyncMap(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ interface{}) bool {
		n++
		return true
	})
	return n
}

// newDeltaValue 构造一条带指定 counterKey / 维度的 V2 stat value（供 delta 测试）。
func newDeltaValue(counterKey uint32, svc string) *plugin.RateLimitStatValueV2 {
	v := plugin.PoolGetRateLimitStatValueV2()
	v.Namespace = "ns1"
	v.Service = svc
	v.Method = "m1"
	v.AppId = "app1"
	v.Uin = "uin1"
	v.Labels = "k=v"
	v.Duration = time.Second
	v.StatKey = plugin.RateLimitStatKeyV2{
		RateLimitStatCounterKeyV2: plugin.RateLimitStatCounterKeyV2{CounterKey: counterKey},
	}
	return v
}

func TestRateLimitCurveReporter_BuildRecordFromDeltas(t *testing.T) {
	Convey("BuildRecordFromDeltas 按 statKey 聚合曲线增量", t, func() {
		r := NewRateLimitCurveReporter(&ReportConfig{RateLimitAppName: "app-x"})

		Convey("空输入返回无 tag 的记录", func() {
			rec := r.BuildRecordFromDeltas(nil)
			So(rec, ShouldNotBeNil)
			So(rec.HasTags(), ShouldBeFalse)
			So(rec.AppName, ShouldEqual, "app-x")
		})

		Convey("同一 statKey 的多条增量应合并为一行并累加", func() {
			v := newDeltaValue(1, "svc1")
			deltas := []CurveDelta{
				{StatValue: v, Passed: 7, Limited: 3},
				{StatValue: v, Passed: 5, Limited: 2},
			}
			rec := r.BuildRecordFromDeltas(deltas)
			So(len(rec.Tags), ShouldEqual, 1)
			// 值格式：limit_count=<limited>&quota_count=<passed>
			So(rec.Tags[0].ValueStr, ShouldEqual, "limit_count=5&quota_count=12")
			So(rec.Tags[0].TagStr, ShouldContainSubstring, "service=svc1")
		})

		Convey("不同 statKey 各成一行", func() {
			d1 := CurveDelta{StatValue: newDeltaValue(1, "svc1"), Passed: 1, Limited: 0}
			d2 := CurveDelta{StatValue: newDeltaValue(2, "svc2"), Passed: 0, Limited: 4}
			rec := r.BuildRecordFromDeltas([]CurveDelta{d1, d2})
			So(len(rec.Tags), ShouldEqual, 2)
			joined := rec.Tags[0].TagStr + "|" + rec.Tags[1].TagStr
			So(joined, ShouldContainSubstring, "service=svc1")
			So(joined, ShouldContainSubstring, "service=svc2")
		})

		Convey("全零增量被跳过", func() {
			d := CurveDelta{StatValue: newDeltaValue(1, "svc1"), Passed: 0, Limited: 0}
			rec := r.BuildRecordFromDeltas([]CurveDelta{d})
			So(rec.HasTags(), ShouldBeFalse)
		})

		Convey("nil StatValue 被跳过，不 panic", func() {
			So(func() {
				rec := r.BuildRecordFromDeltas([]CurveDelta{{StatValue: nil, Passed: 1, Limited: 1}})
				So(rec.HasTags(), ShouldBeFalse)
			}, ShouldNotPanic)
		})
	})
}

func TestBuildValueStr(t *testing.T) {
	Convey("buildValueStr 输出 limit_count/quota_count 格式", t, func() {
		So(buildValueStr(3, 7), ShouldEqual, "limit_count=3&quota_count=7")
		So(strings.HasPrefix(buildValueStr(0, 0), "limit_count=0"), ShouldBeTrue)
	})
}

func TestFileLogger_ReportCurveDeltas_WritesFile(t *testing.T) {
	Convey("共享模式 FileLogger.ReportCurveDeltas 端到端写出 ratelimit_report 日志", t, func() {
		dir := t.TempDir()
		reportPath := filepath.Join(dir, "ratelimit-report.log")
		cfg := &ReportConfig{
			RateLimitAppName:          "app-x",
			ServerAppName:             "srv-x",
			RateLimitReportLogPath:    reportPath,
			RateLimitPrecisionLogPath: filepath.Join(dir, "stat.log"),
			RateLimitEventLogPath:     filepath.Join(dir, "event.log"),
			ServerReportLogPath:       filepath.Join(dir, "server-report.log"),
			LogInterval:               60,
			PrecisionLogInterval:      1,
		}
		fl, err := NewFileLogger(cfg, true /* sharedCollector */)
		So(err, ShouldBeNil)
		// 不调用 Start，避免起后台 ticker；直接验证 delta 驱动路径

		v := newDeltaValue(1, "svc1")
		fl.ReportCurveDeltas([]CurveDelta{
			{StatValue: v, Passed: 7, Limited: 3},
			{StatValue: v, Passed: 5, Limited: 2},
		})

		// zap 默认同步写入 lumberjack，读取文件应命中聚合后的值
		data, rerr := os.ReadFile(reportPath)
		So(rerr, ShouldBeNil)
		content := string(data)
		So(content, ShouldContainSubstring, "app-x")
		So(content, ShouldContainSubstring, "service=svc1")
		So(content, ShouldContainSubstring, "limit_count=5&quota_count=12")

		Convey("空 delta 不写入", func() {
			before, _ := os.ReadFile(reportPath)
			fl.ReportCurveDeltas(nil)
			after, _ := os.ReadFile(reportPath)
			So(len(after), ShouldEqual, len(before))
		})
	})
}

// TestRateLimitCurveReporter_DropCollectorCleanup 验证 drop 后的 collector 清理：
// 共享模式下由每秒的 precision 路径（isCurve=false）读一次后清空 droppedCollectors，
// 避免因曲线路径被 prometheus 接管而永不清理导致的内存泄漏；非共享模式行为保持不变。
func TestRateLimitCurveReporter_DropCollectorCleanup(t *testing.T) {
	Convey("drop collector 后 droppedCollectors 的清理时机", t, func() {
		Convey("共享模式：precision 路径读一次后清空 droppedCollectors", func() {
			r := NewRateLimitCurveReporter(&ReportConfig{RateLimitAppName: "app-x"})
			r.sharedCollector = true
			c := plugin.NewRateLimitStatCollectorV2()
			r.collectors.Store(c.ID(), c)
			So(countSyncMap(r.collectors), ShouldEqual, 1)

			r.DropCollector(c)
			So(countSyncMap(r.collectors), ShouldEqual, 0)        // 从 collectors 移除
			So(countSyncMap(r.droppedCollectors), ShouldEqual, 1) // 进入 dropped

			// 模拟每秒一次的 precision tick（isCurve=false）
			r.MergeAllStatValues(false)
			So(countSyncMap(r.droppedCollectors), ShouldEqual, 0) // 共享模式已清理，无泄漏
		})

		Convey("非共享模式：precision 路径不清理，曲线路径才清理（行为不变）", func() {
			r := NewRateLimitCurveReporter(&ReportConfig{RateLimitAppName: "app-x"})
			r.sharedCollector = false
			c := plugin.NewRateLimitStatCollectorV2()
			r.collectors.Store(c.ID(), c)
			r.DropCollector(c)
			So(countSyncMap(r.droppedCollectors), ShouldEqual, 1)

			r.MergeAllStatValues(false) // precision：不清理
			So(countSyncMap(r.droppedCollectors), ShouldEqual, 1)

			r.MergeAllStatValues(true) // 曲线：清理
			So(countSyncMap(r.droppedCollectors), ShouldEqual, 0)
		})
	})
}

// TestFileLogger_DropCollectorRemovesFromCollectors 验证 FileLogger.DropCollector
// 同时从 collectors 移除并移入 droppedCollectors（此前漏了 collectors.Delete，
// 导致已关闭 stream 的 collector 永久留在 collectors 中被 precision 路径反复处理）。
func TestFileLogger_DropCollectorRemovesFromCollectors(t *testing.T) {
	Convey("FileLogger.DropCollector 从 collectors 移除并移入 droppedCollectors", t, func() {
		dir := t.TempDir()
		cfg := &ReportConfig{
			RateLimitAppName:          "app-x",
			ServerAppName:             "srv-x",
			RateLimitReportLogPath:    filepath.Join(dir, "ratelimit-report.log"),
			RateLimitPrecisionLogPath: filepath.Join(dir, "stat.log"),
			RateLimitEventLogPath:     filepath.Join(dir, "event.log"),
			ServerReportLogPath:       filepath.Join(dir, "server-report.log"),
			LogInterval:               60,
			PrecisionLogInterval:      1,
		}
		fl, err := NewFileLogger(cfg, true /* sharedCollector */)
		So(err, ShouldBeNil)

		c := plugin.NewRateLimitStatCollectorV2()
		fl.RegisterCollector(c)
		So(countSyncMap(fl.rateLimitCurveReporter.collectors), ShouldEqual, 1)

		fl.DropCollector(c)
		So(countSyncMap(fl.rateLimitCurveReporter.collectors), ShouldEqual, 0)
		So(countSyncMap(fl.rateLimitCurveReporter.droppedCollectors), ShouldEqual, 1)

		Convey("DropCollector(nil) 安全", func() {
			So(func() { fl.DropCollector(nil) }, ShouldNotPanic)
		})
	})
}
