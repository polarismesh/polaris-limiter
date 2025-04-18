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
	"encoding/json"
	"fmt"
	"sync"

	"github.com/modern-go/reflect2"
	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/polarismesh/polaris-limiter/plugin"
)

const (
	logReportLogSize    = 100
	logReportLogBackups = 20
	logReportLogMaxAge  = 10
)

// 日志上报的池化值
var logStatValuePool = &sync.Pool{}

// PoolGetLogStatValue 从池中获取上报值对象
func PoolGetLogStatValue() *LogStatValue {
	value := logStatValuePool.Get()
	if !reflect2.IsNil(value) {
		return value.(*LogStatValue)
	}
	return &LogStatValue{}
}

// PoolPutLogStatValue 对象重新入池
func PoolPutLogStatValue(value *LogStatValue) {
	logStatValuePool.Put(value)
}

// LogStatKey 日志上报的Key
type LogStatKey struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Method    string `json:"method"`
	AppId     string `json:"app_id"`
	Uin       string `json:"uin"`
	Labels    string `json:"labels"`
	Duration  int64  `json:"duration"`
}

// String ToString方法
func (s LogStatKey) String() string {
	return fmt.Sprintf(
		"namespace=%s&service=%s&labels=%s", s.Namespace, s.Service, s.Labels)
}

// LogStatValue 日志总体统计内容
type LogStatValue struct {
	LogStatKey
	Passed     int64  `json:"passed"`
	Total      int64  `json:"total"`
	Limited    int64  `json:"limited"`
	TimeNumber int64  `json:"time_number"`
	TimeStr    string `json:"time_str"`
}

// String ToString方法
func (i LogStatValue) String() string {
	jsonStr, _ := json.Marshal(&i)
	return string(jsonStr)
}

// LogStatHandler 统计日志打印处理器
type LogStatHandler struct {
	// 上报所用的日志输出类
	reportLogger *zap.Logger
}

// NewLogStatHandler 创建曲线上报系统
func NewLogStatHandler(cfg *ReportConfig) *LogStatHandler {
	reporter := &LogStatHandler{}
	reporter.initLogger(cfg.RateLimitPrecisionLogPath)
	log.Infof("succeed to init logStatReporter, logPath=%s", cfg.RateLimitPrecisionLogPath)
	return reporter
}

// 初始化日志上报
func (l *LogStatHandler) initLogger(reportLogPath string) {
	// 日志的encode，不打印日志级别和时间戳
	encCfg := zapcore.EncoderConfig{
		NameKey:        "scope",                       // json时日志记录器键
		CallerKey:      "caller",                      // json时日志文件信息键
		MessageKey:     "msg",                         // json时日志消息键
		StacktraceKey:  "stack",                       // json时堆栈键
		LineEnding:     zapcore.DefaultLineEnding,     // console时日志换行符
		EncodeLevel:    zapcore.LowercaseLevelEncoder, // console时小写编码器 (info INFO)
		EncodeCaller:   zapcore.ShortCallerEncoder,    // 日志文件信息（包/文件.go:行号）
		EncodeDuration: zapcore.StringDurationEncoder, // 时间序列化
	}
	w := zapcore.AddSync(&lumberjack.Logger{
		Filename:   reportLogPath,       // 输出文件
		MaxSize:    logReportLogSize,    // 日志文件最大大小（单位：MB）
		MaxBackups: logReportLogBackups, // 保留的旧日志文件最大数量
		MaxAge:     logReportLogMaxAge,  // 保存日期
	})
	core := zapcore.NewCore(zapcore.NewConsoleEncoder(encCfg), w, zap.InfoLevel)
	// 不打印路径
	logger := zap.New(core)
	l.reportLogger = logger
}

// LogPrecisionRecord 打印精度上报记录
func (l *LogStatHandler) LogPrecisionRecord(values map[interface{}]plugin.RateLimitStatValue) int {
	if len(values) == 0 {
		return 0
	}
	var total int
	for _, value := range values {
		rateLimitData := value.GetPrecisionData()
		limited := rateLimitData.GetLimited()
		//if limited == 0 {
		//	// 没有发生限流，则不打印精度日志
		//	continue
		//}
		total++
		logStatValue := PoolGetLogStatValue()
		logStatValue.Namespace = value.GetNamespace()
		logStatValue.Service = value.GetService()
		logStatValue.Method = value.GetMethod()
		logStatValue.AppId = value.GetAppId()
		logStatValue.Uin = value.GetUin()
		logStatValue.Labels = value.GetLabels()
		logStatValue.Duration = value.GetDuration().Milliseconds()
		logStatValue.Passed = rateLimitData.GetPassed()
		logStatValue.Limited = limited
		logStatValue.Total = value.GetTotal()
		logStatValue.TimeNumber = rateLimitData.GetLastFetchTime()
		logStatValue.TimeStr = utils.TimestampMsToUtcIso8601(logStatValue.TimeNumber)
		l.reportLogger.Info(logStatValue.String())
		PoolPutLogStatValue(logStatValue)
	}
	return total
}
