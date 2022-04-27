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
	"fmt"
	"net"
	"net/http"

	"github.com/emicklei/go-restful"

	"github.com/polarismesh/polaris-limiter/pkg/log"
)

// Server http server
type Server struct {
	ip      string
	port    uint32
	server  *http.Server
	handler *restful.Container
}

// Initialize subInitialize
func (h *Server) Initialize(option map[string]interface{}) error {
	h.ip = option["ip"].(string)
	h.port = uint32(option["port"].(int))
	return nil
}

// Run running
func (h *Server) Run(errCh chan error) {
	address := fmt.Sprintf("%s:%d", h.ip, h.port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Errorf("[HTTP] net listen(%s) err: %s", address, err.Error())
		errCh <- err
		return
	}
	// handler
	h.initHandler()
	// http server
	server := http.Server{Addr: address, Handler: h.handler}
	h.server = &server
	if err := h.server.Serve(listener); err != nil {
		log.Errorf("[HTTP] server serve err: %s", err.Error())
		errCh <- err
		return
	}
}

// Stop server
func (h *Server) Stop() {
	if h.server != nil {
		_ = h.server.Close()
	}
}

// GetProtocol get protocol
func (h *Server) GetProtocol() string {
	return "http"
}

// GetPort 	get port
func (h *Server) GetPort() uint32 {
	return h.port
}
