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
	"net/http"

	"github.com/emicklei/go-restful"

	"github.com/polarismesh/polaris-limiter/pkg/health"
)

// Liveness 进程存活探测，不触碰磁盘。
func (h *Server) Liveness(req *restful.Request, rsp *restful.Response) {
	writeHealth(rsp, health.LivenessReport())
}

// Readiness 业务就绪探测，只读 IOHang 缓存状态。
func (h *Server) Readiness(req *restful.Request, rsp *restful.Response) {
	writeHealth(rsp, health.ReadinessReport())
}

func writeHealth(rsp *restful.Response, report health.Report) {
	code := http.StatusOK
	if report.Status != health.StatusUp {
		code = http.StatusServiceUnavailable
	}
	_ = rsp.WriteHeaderAndJson(code, report, restful.MIME_JSON)
}
