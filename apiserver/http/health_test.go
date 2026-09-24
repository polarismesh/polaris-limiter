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

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/polarismesh/polaris-limiter/pkg/health"
)

func TestHealthEndpoints(t *testing.T) {
	Convey("HTTP 健康检查端点", t, func() {
		defer health.SetDefault(nil)
		h := &Server{}
		h.initHandler()

		Convey("/liveness 始终 200 且不含 ioHang", func() {
			req := httptest.NewRequest(http.MethodGet, "/liveness", nil)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			So(rec.Code, ShouldEqual, http.StatusOK)
			var body health.Report
			So(json.Unmarshal(rec.Body.Bytes(), &body), ShouldBeNil)
			So(body.Status, ShouldEqual, health.StatusUp)
			_, hasIO := body.Components["ioHang"]
			So(hasIO, ShouldBeFalse)
		})

		Convey("/ 保持兼容字符串", func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			So(rec.Code, ShouldEqual, http.StatusOK)
			So(rec.Body.String(), ShouldEqual, "polaris limit server")
		})

		Convey("探测写 hang 住时 /readiness 返回 503", func() {
			w := newHangWriter()
			defer close(w.release)
			d := health.NewIOHangDetector(health.Config{Timeout: time.Millisecond}.WithDefaults(), w)
			go func() { _ = d.ProbeOnce() }()
			<-w.started
			time.Sleep(20 * time.Millisecond)
			health.SetDefault(health.NewChecker(health.Config{Enable: boolPtr(true)}.WithDefaults(), d))

			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			So(rec.Code, ShouldEqual, http.StatusServiceUnavailable)
			var body health.Report
			So(json.Unmarshal(rec.Body.Bytes(), &body), ShouldBeNil)
			So(body.Status, ShouldEqual, health.StatusDown)
			So(body.Components["ioHang"].Status, ShouldEqual, health.StatusDown)
		})
	})
}

type hangWriter struct {
	started chan struct{}
	release chan struct{}
}

func newHangWriter() *hangWriter {
	return &hangWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (w *hangWriter) WriteAndSync([]byte) error {
	close(w.started)
	<-w.release
	return nil
}

func boolPtr(v bool) *bool { return &v }
