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

import (
	"context"
	"go.uber.org/zap"
)

// 封装RequestID日志打印域
func ZapRequestID(ctx context.Context) zap.Field {
	return zap.String("request-id", ParseRequestID(ctx))
}

// 封装clientAddr的日志打印域
func ZapClientAddr(ctx context.Context) zap.Field {
	return zap.String("client-addr", ParseClientAddr(ctx))
}

// 封装userAgent的日志打印域
func ZapUserAgent(ctx context.Context) zap.Field {
	return zap.String("user-agent", ParseUserAgent(ctx))
}

// 封装method的日志打印域
func ZapMethod(method string) zap.Field {
	return zap.String("method", method)
}

// 封装key的打印域
func ZapLimitKey(key string) zap.Field {
	return zap.String("limiter-key", key)
}

// 封装service的打印域
func ZapLimitService(service string, namespace string) zap.Field {
	return zap.String("service", namespace+":"+service)
}

// 封装消息ID
func ZapMsgId(msgId int64) zap.Field {
	return zap.Int64("msgId", msgId)
}

// 封装错误码
func ZapCode(code uint32) zap.Field {
	return zap.Uint32("code", code)
}