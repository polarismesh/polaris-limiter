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
	"context"
	"net/http"
	"strings"

	"github.com/emicklei/go-restful"
	"github.com/golang/protobuf/jsonpb"
	"github.com/golang/protobuf/proto"

	api "github.com/polarismesh/polaris-limit/api/v2"
	"github.com/polarismesh/polaris-limit/pkg/log"
	"github.com/polarismesh/polaris-limit/pkg/utils"
)

// http请求封装器
type Handler struct {
	req *restful.Request
	rsp *restful.Response
	ctx context.Context
}

// 解析请求proto
func (h *Handler) ParseProto(obj proto.Message) error {
	ctx := requestCtx(h.req)
	h.ctx = ctx

	if err := jsonpb.Unmarshal(h.req.Request.Body, obj); err != nil {
		log.Error(err.Error(), utils.ZapRequestID(ctx))
		return err
	}
	return nil
}

// 回复proto
func (h *Handler) ResponseProto(code api.Code, obj proto.Message) {
	if obj == nil {
		h.rsp.WriteHeader(http.StatusOK)
		return
	}
	status := api.Code2HTTPStatus(code)
	if status != http.StatusOK {
		log.Error(obj.String(), utils.ZapRequestID(h.ctx))
	}

	h.rsp.AddHeader("Request-Id", utils.ParseRequestID(h.ctx))
	h.rsp.WriteHeader(status)
	m := jsonpb.Marshaler{Indent: " "}
	if err := m.Marshal(h.rsp, obj); err != nil {
		log.Error(err.Error(), utils.ZapRequestID(h.ctx))
		return
	}

	return
}

// 从request构造ctx
func requestCtx(req *restful.Request) context.Context {
	ctx := context.Background()

	requestID := req.HeaderParameter("Request-Id")
	ctx = utils.WithRequestID(ctx, requestID)

	ctx = utils.WithClientAddr(ctx, req.Request.RemoteAddr)

	addrSlice := strings.Split(req.Request.RemoteAddr, ":")
	if len(addrSlice) == 2 {
		ctx = utils.WithClientIP(ctx, addrSlice[0])
	}

	return ctx

}
