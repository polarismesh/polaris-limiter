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

package ratelimitv2

import (
	"time"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/config"
)

// 默认滑窗数量
const (
	// MaxSlideCount 最大滑窗
	MaxSlideCount = config.MaxSlideCount
)

// CheckRateLimitReportRequest 检查限流上报请求参数
func CheckRateLimitReportRequest(req *apiv2.RateLimitReportRequest) *apiv2.TimedRateLimitReportResponse {
	if req.GetClientKey() == 0 {
		return apiv2.NewRateLimitReportResponse(apiv2.InvalidClientKey)
	}
	if req.GetTimestamp() == 0 {
		return apiv2.NewRateLimitReportResponse(apiv2.InvalidTimestamp)
	}
	if len(req.GetQuotaUses()) == 0 {
		return apiv2.NewRateLimitReportResponse(apiv2.InvalidUsedLimit)
	}
	for _, quotaUsed := range req.GetQuotaUses() {
		if quotaUsed.GetCounterKey() == 0 {
			return apiv2.NewRateLimitReportResponse(apiv2.InvalidCounterKey)
		}
	}
	return nil
}

// 通用检查限流请求的参数
func checkInitRequest(
	req *apiv2.RateLimitInitRequest, defaultSlideCount uint32) (*apiv2.RateLimitInitResponse, time.Duration) {
	if len(req.GetTarget().GetService()) == 0 {
		return apiv2.NewRateLimitInitResponse(apiv2.InvalidServiceName, req.GetTarget()), 0
	}
	if len(req.GetTarget().GetNamespace()) == 0 {
		return apiv2.NewRateLimitInitResponse(apiv2.InvalidNamespace, req.GetTarget()), 0
	}
	if len(req.GetTotals()) == 0 {
		return apiv2.NewRateLimitInitResponse(apiv2.InvalidTotalLimit, req.GetTarget()), 0
	}
	var maxDuration time.Duration
	for _, total := range req.GetTotals() {
		if total.GetDuration() == 0 {
			return apiv2.NewRateLimitInitResponse(apiv2.InvalidDuration, req.GetTarget()), 0
		}
		timeDuration := time.Duration(total.GetDuration()) * time.Second
		if maxDuration < timeDuration {
			maxDuration = timeDuration
		}
	}
	if req.GetSlideCount() == 0 {
		req.SlideCount = defaultSlideCount
	} else if req.GetSlideCount() > MaxSlideCount {
		return apiv2.NewRateLimitInitResponse(apiv2.InvalidSlideCount, req.GetTarget()), 0
	}
	if req.GetMode() != apiv2.Mode_ADAPTIVE && req.GetMode() != apiv2.Mode_BATCH_OCCUPY {
		return apiv2.NewRateLimitInitResponse(apiv2.InvalidMode, req.GetTarget()), 0
	}
	return nil, maxDuration
}

// CheckRateLimitInitRequest 检查限流初始化请求参数
func CheckRateLimitInitRequest(
	req *apiv2.RateLimitInitRequest, defaultSlideCount uint32) (*apiv2.RateLimitInitResponse, time.Duration) {
	if len(req.GetClientId()) == 0 {
		return apiv2.NewRateLimitInitResponse(apiv2.InvalidClientId, req.GetTarget()), 0
	}
	return checkInitRequest(req, defaultSlideCount)
}

// CheckRateLimitBatchInitRequest 检查限流初始化请求参数
func CheckRateLimitBatchInitRequest(
	req *apiv2.RateLimitInitRequest, defaultSlideCount uint32) (*apiv2.RateLimitInitResponse, time.Duration) {
	if len(req.GetTarget().GetLabelsList()) == 0 {
		return apiv2.NewRateLimitInitResponse(apiv2.InvalidLabels, req.GetTarget()), 0
	}
	return checkInitRequest(req, defaultSlideCount)
}
