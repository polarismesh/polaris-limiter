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

package utils

import "context"

// requestID ctx
type requestIDCtx struct{}

// client ip ctx
type clientIPCtx struct{}

// client ip ctx
type structClientIPCtx struct{}

// client address ctx
type clientAddrCtx struct{}

//type ClientPortCtx struct{}

// user agent
type userAgent struct{}

// 增加requestID
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDCtx{}, requestID)
}

// 从ctx中获取requestID
func ParseRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	id, ok := ctx.Value(requestIDCtx{}).(string)
	if !ok {
		return ""
	}

	return id
}

// ctx增加客户端IP
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPCtx{}, ip)
}

// ctx增加客户端IP
func WithStructClientIP(ctx context.Context, ip *IPAddress) context.Context {
	return context.WithValue(ctx, structClientIPCtx{}, ip)
}

// 从ctx中获取客户端IP
func ParseClientIP(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ip, ok := ctx.Value(clientIPCtx{}).(string)
	if !ok {
		return ""
	}

	return ip
}

// ctx增加客户端IP
func ParseStructClientIP(ctx context.Context) *IPAddress {
	if ctx == nil {
		return &IPAddress{}
	}
	ip, ok := ctx.Value(structClientIPCtx{}).(*IPAddress)
	if !ok {
		return &IPAddress{}
	}

	return ip
}

// ctx增加客户端连接地址
func WithClientAddr(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, clientAddrCtx{}, addr)
}

// 从ctx中获取客户端连接地址，ip:port形式
func ParseClientAddr(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	addr, ok := ctx.Value(clientAddrCtx{}).(string)
	if !ok {
		return ""
	}

	return addr
}

// user agent
func WithUserAgent(ctx context.Context, agent string) context.Context {
	return context.WithValue(ctx, userAgent{}, agent)
}

// parse agent
func ParseUserAgent(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	agent, ok := ctx.Value(userAgent{}).(string)
	if !ok {
		return ""
	}

	return agent
}
