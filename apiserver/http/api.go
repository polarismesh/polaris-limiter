/**
 * Tencent is pleased to support the open source community by making Polaris available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company. All rights reserved.
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
)

// 初始化http handler
func (h *Server) initHandler() {
	h.handler = restful.NewContainer()

	h.handler.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	h.handler.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	h.handler.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	h.handler.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))

	index := new(restful.WebService)
	index.Route(index.GET("/").To(h.Index))
	h.handler.Add(index)

	h.initMaintainHandler()
}

// 默认的handler
func (h *Server) Index(req *restful.Request, rsp *restful.Response) {
	_, _ = rsp.Write([]byte("polaris limit server"))
}
