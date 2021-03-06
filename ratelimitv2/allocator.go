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

import apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"

// QuotaAllocator 请求分配器
type QuotaAllocator interface {
	// Mode 返回分配器所属的模式
	Mode() apiv2.Mode
	// Allocate 分配配额
	Allocate(client Client, quotaSum *apiv2.QuotaSum, clientTimeMs int64, serverTimeMicro int64) *apiv2.QuotaLeft
}
