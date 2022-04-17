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

package file

import (
	"errors"
	"fmt"
)

const (
	minLogInterval             = 1
	minPrecisionLogInterval    = 1
	defaultLogReportLogSize    = 100
	defaultLogReportLogBackups = 20
	defaultLogReportLogMaxAge  = 10
)

//智研上报配置项
type ReportConfig struct {
	RateLimitAppName          string `json:"ratelimit-app-name"`
	RateLimitReportLogPath    string `json:"ratelimit_report_log_path"`
	RateLimitPrecisionLogPath string `json:"ratelimit_precision_log_path"`
	RateLimitEventLogPath     string `json:"ratelimit_event_log_path"`
	ServerAppName             string `json:"server-app-name"`
	ServerReportLogPath       string `json:"server_report_log_path"`
	PrecisionLogInterval      int    `json:"precision_log_interval"`
	LogInterval               int    `json:"log_interval"`
	LogSize                   int    `json:"log_size"`
	LogBackups                int    `json:"log_backups"`
	LogMaxAge                 int    `json:"log_max_age"`
}

//校验
func (r *ReportConfig) Validate() error {
	if len(r.RateLimitAppName) == 0 {
		return errors.New("ratelimit-app-name is empty")
	}
	if len(r.ServerAppName) == 0 {
		return errors.New("server-app-name is empty")
	}
	if r.LogInterval < minLogInterval {
		return errors.New(fmt.Sprintf("log_interval must be greater than %d", minLogInterval))
	}
	if r.PrecisionLogInterval < minPrecisionLogInterval {
		return errors.New(fmt.Sprintf("precision_log_interval must be greater than %d", minPrecisionLogInterval))
	}
	if len(r.RateLimitReportLogPath) == 0 {
		return errors.New("ratelimit_report_log_path is empty")
	}
	if len(r.RateLimitPrecisionLogPath) == 0 {
		return errors.New("ratelimit_precision_log_path is empty")
	}
	if len(r.RateLimitEventLogPath) == 0 {
		return errors.New("ratelimit_event_log_path is empty")
	}
	if len(r.ServerReportLogPath) == 0 {
		return errors.New("server_report_log_path is empty")
	}
	if r.LogSize == 0 {
		r.LogSize = defaultLogReportLogSize
	}
	if r.LogBackups == 0 {
		r.LogBackups = defaultLogReportLogBackups
	}
	if r.LogMaxAge == 0 {
		r.LogMaxAge = defaultLogReportLogMaxAge
	}
	return nil
}
