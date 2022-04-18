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

package v2

import (
	"github.com/polarismesh/polaris-limiter/pkg/utils"
)

//带上精准时间戳的上报消息
type TimedRateLimitReportResponse struct {
	RateLimitReportResponse
	//创建时间，精度为微秒
	createTimeMicro int64
}

//转换为真实的上报请求
func (t *TimedRateLimitReportResponse) ToRateLimitReportResponse() *RateLimitReportResponse {
	return &t.RateLimitReportResponse
}

//获取创建时间
func (t *TimedRateLimitReportResponse) CreateTimeMicro() int64 {
	return t.createTimeMicro
}

// 新建一个初始化回复结构体
func NewRateLimitInitResponse(code Code, target *LimitTarget) *RateLimitInitResponse {
	return &RateLimitInitResponse{Code: uint32(code), Target: target, Timestamp: utils.CurrentMillisecond()}
}

// 新建一个初始化回复结构体
func NewRateLimitBatchInitResponse(code Code) *RateLimitBatchInitResponse {
	return &RateLimitBatchInitResponse{Code: uint32(code), Timestamp: utils.CurrentMillisecond()}
}

// 新建一个上报回复结构体
func NewRateLimitReportResponse(code Code) *TimedRateLimitReportResponse {
	curTimeMicro := utils.CurrentMicrosecond()
	return &TimedRateLimitReportResponse{
		RateLimitReportResponse: RateLimitReportResponse{
			Code: uint32(code), Timestamp: curTimeMicro / 1e3,
		},
		createTimeMicro: curTimeMicro,
	}
}
