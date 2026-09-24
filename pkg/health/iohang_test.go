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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

type nopSink struct{}

func (nopSink) WriteAndSync([]byte) error { return nil }

type errSink struct{ err error }

func (s errSink) WriteAndSync([]byte) error { return s.err }

type blockingSink struct {
	n         atomic.Int32
	startOnce sync.Once
	started   chan struct{}
	release   chan struct{}
}

func newBlockingSink() *blockingSink {
	return &blockingSink{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingSink) WriteAndSync([]byte) error {
	s.n.Add(1)
	s.startOnce.Do(func() { close(s.started) })
	<-s.release
	return nil
}

func testDetectorCfg() Config {
	return Config{
		Interval:  10 * time.Millisecond,
		Timeout:   40 * time.Millisecond,
		Staleness: 200 * time.Millisecond,
	}.WithDefaults()
}

// downDetector 返回一个 staleness 已过期、处于 DOWN 的检测器。
func downDetector() *IOHangDetector {
	d := NewIOHangDetector(testDetectorCfg(), nopSink{})
	_ = d.ProbeOnce()
	d.lastDoneAt.Store(time.Now().Add(-time.Hour).UnixNano())
	return d
}

func TestIOHangDetectorStatus(t *testing.T) {
	Convey("IOHangDetector 状态机", t, func() {
		Convey("尚未完成过探测且 inflight 未超时视为 warming-up UP", func() {
			d := NewIOHangDetector(testDetectorCfg(), nopSink{})
			up, details := d.Status()
			So(up, ShouldBeTrue)
			So(details["reason"], ShouldEqual, "warming-up")
		})

		Convey("探测成功后为 UP 并带 lastSuccessMs", func() {
			d := NewIOHangDetector(testDetectorCfg(), nopSink{})
			So(d.ProbeOnce(), ShouldBeNil)
			up, details := d.Status()
			So(up, ShouldBeTrue)
			So(details["lastSuccessAt"], ShouldNotBeEmpty)
			_, ok := details["lastSuccessMs"].(float64)
			So(ok, ShouldBeTrue)
		})

		Convey("inflight 超过 timeout 判 DOWN", func() {
			sink := newBlockingSink()
			defer close(sink.release)
			d := NewIOHangDetector(testDetectorCfg(), sink)
			d.kickProbe()
			<-sink.started
			time.Sleep(100 * time.Millisecond)
			up, details := d.Status()
			So(up, ShouldBeFalse)
			So(details["reason"], ShouldContainSubstring, "inflight")
		})

		Convey("上次探测完成超过 staleness 判 DOWN", func() {
			up, details := downDetector().Status()
			So(up, ShouldBeFalse)
			So(details["reason"], ShouldContainSubstring, "staleness")
		})

		Convey("写入立即报错不算 IOHang：UP 并带 lastError", func() {
			d := NewIOHangDetector(testDetectorCfg(), errSink{err: errors.New("no space left on device")})
			So(d.ProbeOnce(), ShouldNotBeNil)
			up, details := d.Status()
			So(up, ShouldBeTrue)
			So(details["lastError"], ShouldContainSubstring, "no space")
		})

		Convey("写错误后再次成功应清除 lastError", func() {
			sink := &toggleSink{err: errors.New("disk")}
			d := NewIOHangDetector(testDetectorCfg(), sink)
			_ = d.ProbeOnce()
			sink.err = nil
			So(d.ProbeOnce(), ShouldBeNil)
			_, details := d.Status()
			_, hasErr := details["lastError"]
			So(hasErr, ShouldBeFalse)
		})

		Convey("inflight 未完成时不启第二轮探测", func() {
			sink := newBlockingSink()
			defer close(sink.release)
			d := NewIOHangDetector(testDetectorCfg(), sink)
			d.kickProbe()
			<-sink.started
			d.kickProbe()
			d.kickProbe()
			So(sink.n.Load(), ShouldEqual, 1)
		})
	})
}

func TestIOHangDetectorWaitFirstResult(t *testing.T) {
	Convey("WaitFirstResult 等待首次探测结论", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		Convey("探测正常完成后立即返回且不再 warming-up", func() {
			d := NewIOHangDetector(testDetectorCfg(), nopSink{})
			d.Start(ctx)
			d.WaitFirstResult(ctx)
			up, details := d.Status()
			So(up, ShouldBeTrue)
			So(details["reason"], ShouldBeNil)
		})

		Convey("写入报错也算有结论，返回 UP", func() {
			d := NewIOHangDetector(testDetectorCfg(), errSink{err: errors.New("read-only file system")})
			d.Start(ctx)
			d.WaitFirstResult(ctx)
			So(d.Healthy(), ShouldBeTrue)
		})

		Convey("首次探测 hang 时在 timeout 附近返回 DOWN", func() {
			sink := newBlockingSink()
			defer close(sink.release)
			d := NewIOHangDetector(testDetectorCfg(), sink)
			d.Start(ctx)
			begin := time.Now()
			d.WaitFirstResult(ctx)
			So(d.Healthy(), ShouldBeFalse)
			So(time.Since(begin), ShouldBeLessThan, 2*d.timeout+firstResultPoll)
		})

		Convey("检测器未 Start 时最多等待 2*timeout", func() {
			d := NewIOHangDetector(testDetectorCfg(), nopSink{})
			begin := time.Now()
			d.WaitFirstResult(ctx)
			So(time.Since(begin), ShouldBeGreaterThanOrEqualTo, 2*d.timeout)
		})
	})
}

type toggleSink struct{ err error }

func (s *toggleSink) WriteAndSync([]byte) error { return s.err }
