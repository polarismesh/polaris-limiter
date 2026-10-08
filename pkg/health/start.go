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

package health

import "context"

// Start 按配置启动 IOHang 检测并设为默认 Checker。
// enable=false 时仍注入 Checker，/readiness 仅返回 basic。
func Start(ctx context.Context, cfg Config) {
	cfg = cfg.WithDefaults()
	var det *IOHangDetector
	if cfg.Enabled() {
		det = NewIOHangDetector(cfg, newProbeSink(cfg))
		det.Start(ctx)
	}
	SetDefault(NewChecker(cfg, det))
}
