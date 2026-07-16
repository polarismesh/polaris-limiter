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
	"net/http/pprof"

	"github.com/emicklei/go-restful"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/plugin"
)

// metricsGatherer 由 statis 插件实现的可选接口：返回 prometheus.Gatherer 用于
// 暴露 /metrics。未实现该接口的插件（echo / file）走默认全局 gatherer。
type metricsGatherer interface {
	Registry() *prometheus.Registry
}

// 初始化http handler
func (h *Server) initHandler() {
	h.handler = restful.NewContainer()

	h.handler.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	h.handler.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	h.handler.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	h.handler.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))

	h.handler.Handle("/metrics", buildMetricsHandler())

	index := new(restful.WebService)
	index.Route(index.GET("/").To(h.Index))
	h.handler.Add(index)

	h.initMaintainHandler()
}

// buildMetricsHandler 构造 /metrics 处理器：优先使用当前 statis 插件提供的
// 独立 Registry；如果当前 statis 不提供（如 echo / file），降级为默认全局 gatherer。
func buildMetricsHandler() http.Handler {
	statis, err := plugin.GetStatis()
	if err != nil {
		log.Warnf("[HTTP] get statis plugin for /metrics failed: %s, fall back to default gatherer", err.Error())
		return promhttp.Handler()
	}
	if mg, ok := statis.(metricsGatherer); ok && mg.Registry() != nil {
		return promhttp.HandlerFor(mg.Registry(), promhttp.HandlerOpts{})
	}
	return promhttp.Handler()
}

// Index 默认的handler
func (h *Server) Index(req *restful.Request, rsp *restful.Response) {
	_, _ = rsp.Write([]byte("polaris limit server"))
}
