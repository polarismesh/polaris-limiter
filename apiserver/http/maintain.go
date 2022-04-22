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
	"github.com/emicklei/go-restful"
)

// 初始化运维接口的handler
func (h *Server) initMaintainHandler() {
	maintain := new(restful.WebService)
	maintain.Path("/maintain").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)

	maintain.Route(maintain.GET("/counters/total").To(h.GetCountersTotal))
	maintain.Route(maintain.GET("/counters/keys").To(h.ListCountersKeys))
	maintain.Route(maintain.GET("/counter/stat").To(h.GetCounterStat))
	h.handler.Add(maintain)
}

// GetCountersTotal 获取本地缓存的counter个数
func (h *Server) GetCountersTotal(req *restful.Request, rsp *restful.Response) {
	// total := h.ratelimitServer.GetCountersTotal()
	total := 0
	var out struct {
		Count int
	}
	out.Count = total

	_ = rsp.WriteAsJson(out)
}

// ListCountersKeys 获取本地缓存的counter的key列表
// 展示所有缓存数据的信息
// 只返回本地信息，不返回远端信息（避免数据太多，占住太多请求远端的资源）
func (h *Server) ListCountersKeys(req *restful.Request, rsp *restful.Response) {
	// out, err := h.ratelimitServer.ListCountersInfo()
	// if err != nil {
	//	_ = rsp.WriteError(http.StatusInternalServerError, err)
	//	return
	// }

	// _ = rsp.WriteAsJson(out)
	_, _ = rsp.Write([]byte("{}"))
}

// GetCounterStat 获取counter本地和远端的信息
func (h *Server) GetCounterStat(req *restful.Request, rsp *restful.Response) {
	// local, remote, err := h.ratelimitServer.GetCounterStat(req.QueryParameter("key"))
	// if err != nil {
	//	_ = rsp.WriteError(http.StatusInternalServerError, err)
	//	return
	// }
	var local interface{} = nil
	var remote interface{} = nil
	var out struct {
		Local  interface{}
		Remote interface{}
	}
	out.Local = local
	out.Remote = remote
	_ = rsp.WriteAsJson(out)
}
