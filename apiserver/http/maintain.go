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
	"fmt"
	"net/http"
	"strconv"

	"github.com/emicklei/go-restful"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/ratelimitv2"
)

// 初始化运维接口的handler
func (h *Server) initMaintainHandler() {
	maintain := new(restful.WebService)
	maintain.Path("/maintain").Consumes(restful.MIME_JSON).Produces(restful.MIME_JSON)

	maintain.Route(maintain.GET("/counters/total").To(h.GetCountersTotal))
	maintain.Route(maintain.GET("/counters/keys").To(h.ListCountersKeys))
	maintain.Route(maintain.GET("/counter/stat").To(h.GetCounterStat))
	maintain.Route(maintain.GET("/clients/total").To(h.GetClientsTotal))
	maintain.Route(maintain.GET("/clients/keys").To(h.ListClientsKeys))
	maintain.Route(maintain.GET("/client/stat").To(h.GetClientStat))
	h.handler.Add(maintain)
}

// parsePagination 从请求中解析 offset 和 limit 查询参数，默认 offset=0, limit=100
func parsePagination(req *restful.Request) (offset, limit int, err error) {
	offset = 0
	limit = 0
	if v := req.QueryParameter("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("invalid offset: %s", v)
		}
	}
	if v := req.QueryParameter("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 0 {
			return 0, 0, fmt.Errorf("invalid limit: %s", v)
		}
	}
	return offset, limit, nil
}

// GetCountersTotal 获取本地缓存的counter个数
func (h *Server) GetCountersTotal(req *restful.Request, rsp *restful.Response) {
	rateLimitServer, err := ratelimitv2.GetRateLimitServer()
	if err != nil {
		_ = rsp.WriteError(http.StatusServiceUnavailable, err)
		return
	}
	var out struct {
		Count int `json:"count"`
	}
	out.Count = rateLimitServer.CounterMng().CounterCount()
	_ = rsp.WriteAsJson(out)
}

// ListCountersKeys 获取本地缓存的counter的key列表，支持分页（offset、limit 查询参数，limit 默认 100，0 表示不限制）
func (h *Server) ListCountersKeys(req *restful.Request, rsp *restful.Response) {
	offset, limit, err := parsePagination(req)
	if err != nil {
		_ = rsp.WriteError(http.StatusBadRequest, err)
		return
	}
	rateLimitServer, err := ratelimitv2.GetRateLimitServer()
	if err != nil {
		_ = rsp.WriteError(http.StatusServiceUnavailable, err)
		return
	}
	identifiers := rateLimitServer.CounterMng().ListCounterIdentifiers(offset, limit)
	type counterKey struct {
		Key       uint32 `json:"key"`
		Namespace string `json:"namespace"`
		Service   string `json:"service"`
		Labels    string `json:"labels"`
		Duration  string `json:"duration"`
	}
	out := make([]counterKey, 0, len(identifiers))
	for _, item := range identifiers {
		out = append(out, counterKey{
			Key:       item.Key,
			Namespace: item.Identifier.Namespace,
			Service:   item.Identifier.Service,
			Labels:    item.Identifier.Labels,
			Duration:  item.Identifier.Duration.String(),
		})
	}
	_ = rsp.WriteAsJson(out)
}

// GetCounterStat 获取counter本地和远端的信息
func (h *Server) GetCounterStat(req *restful.Request, rsp *restful.Response) {
	keyStr := req.QueryParameter("key")
	key, err := strconv.ParseUint(keyStr, 10, 32)
	if err != nil {
		_ = rsp.WriteError(http.StatusBadRequest, fmt.Errorf("invalid key: %s", keyStr))
		return
	}
	rateLimitServer, err := ratelimitv2.GetRateLimitServer()
	if err != nil {
		_ = rsp.WriteError(http.StatusServiceUnavailable, err)
		return
	}
	code, counter := rateLimitServer.CounterMng().GetCounter(uint32(key))
	if code != apiv2.ExecuteSuccess {
		_ = rsp.WriteError(http.StatusNotFound, fmt.Errorf("counter key %d not found", key))
		return
	}
	id := counter.Identifier()
	var out struct {
		Namespace      string `json:"namespace"`
		Service        string `json:"service"`
		Labels         string `json:"labels"`
		Duration       string `json:"duration"`
		MaxAmount      uint32 `json:"maxAmount"`
		ClientCount    uint32 `json:"clientCount"`
		LastUpdateTime int64  `json:"lastUpdateTime"`
	}
	out.Namespace = id.Namespace
	out.Service = id.Service
	out.Labels = id.Labels
	out.Duration = id.Duration.String()
	out.MaxAmount = counter.MaxAmount()
	out.ClientCount = counter.ClientCount()
	out.LastUpdateTime = counter.LastUpdateTime()
	_ = rsp.WriteAsJson(out)
}

// GetClientsTotal 获取已连接的客户端数量
func (h *Server) GetClientsTotal(req *restful.Request, rsp *restful.Response) {
	rateLimitServer, err := ratelimitv2.GetRateLimitServer()
	if err != nil {
		_ = rsp.WriteError(http.StatusServiceUnavailable, err)
		return
	}
	var out struct {
		Count int `json:"count"`
	}
	out.Count = rateLimitServer.ClientMng().ClientCount()
	_ = rsp.WriteAsJson(out)
}

// ListClientsKeys 获取所有活跃客户端的标识列表，支持分页（offset、limit 查询参数，limit 默认 100，0 表示不限制）
func (h *Server) ListClientsKeys(req *restful.Request, rsp *restful.Response) {
	offset, limit, err := parsePagination(req)
	if err != nil {
		_ = rsp.WriteError(http.StatusBadRequest, err)
		return
	}
	rateLimitServer, err := ratelimitv2.GetRateLimitServer()
	if err != nil {
		_ = rsp.WriteError(http.StatusServiceUnavailable, err)
		return
	}
	clients := rateLimitServer.ClientMng().ListClients(offset, limit)
	type clientKey struct {
		Key uint32 `json:"key"`
		ID  string `json:"id"`
		IP  string `json:"ip"`
	}
	out := make([]clientKey, 0, len(clients))
	for _, c := range clients {
		out = append(out, clientKey{
			Key: c.ClientKey(),
			ID:  c.ClientId(),
			IP:  c.ClientIP().String(),
		})
	}
	_ = rsp.WriteAsJson(out)
}

// GetClientStat 获取单个客户端详情
func (h *Server) GetClientStat(req *restful.Request, rsp *restful.Response) {
	keyStr := req.QueryParameter("key")
	key, err := strconv.ParseUint(keyStr, 10, 32)
	if err != nil {
		_ = rsp.WriteError(http.StatusBadRequest, fmt.Errorf("invalid key: %s", keyStr))
		return
	}
	rateLimitServer, err := ratelimitv2.GetRateLimitServer()
	if err != nil {
		_ = rsp.WriteError(http.StatusServiceUnavailable, err)
		return
	}
	code, c := rateLimitServer.ClientMng().GetClient(uint32(key))
	if code != apiv2.ExecuteSuccess {
		_ = rsp.WriteError(http.StatusNotFound, fmt.Errorf("client key %d not found", key))
		return
	}
	var out struct {
		Key uint32 `json:"key"`
		ID  string `json:"id"`
		IP  string `json:"ip"`
	}
	out.Key = c.ClientKey()
	out.ID = c.ClientId()
	out.IP = c.ClientIP().String()
	_ = rsp.WriteAsJson(out)
}
