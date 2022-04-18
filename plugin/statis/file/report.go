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
	"fmt"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"strings"
	"time"
)

//上报处理器
type ReportHandler interface {
	//上报
	Report(*ReportRecord)
}

//单条上报记录
type ReportItem struct {
	TagStr string
	ValueStr string
}

//上报数据
type ReportRecord struct {
	AppName string
	Tags    []*ReportItem
}

//是否存在数据
func (r *ReportRecord) HasTags() bool {
	return len(r.Tags) > 0
}

//上报处理器实现
type reportHandler struct {
	loggers map[string]*zap.Logger
}

//新建上报处理器
func NewReportHandler(cfg *ReportConfig) ReportHandler {
	handler := &reportHandler{loggers: make(map[string]*zap.Logger)}
	handler.loggers[cfg.ServerAppName] =
		initLogger(cfg.ServerReportLogPath, cfg.LogSize, cfg.LogBackups, cfg.LogMaxAge)
	handler.loggers[cfg.RateLimitAppName] =
		initLogger(cfg.RateLimitReportLogPath, cfg.LogSize, cfg.LogBackups, cfg.LogMaxAge)
	return handler
}

const pattern = "%s %s %s %s 0\n"

//执行上报
func (r *reportHandler) Report(record *ReportRecord) {
	builder := &strings.Builder{}
	builder.WriteString(time.Now().Format("2006-01-02 15:04:05.000000"))
	builder.WriteString("\n")
	for _, reportItem := range record.Tags {
		builder.WriteString(fmt.Sprintf(
			pattern, record.AppName, utils.ServerAddress, reportItem.TagStr, reportItem.ValueStr))
	}
	r.loggers[record.AppName].Info(builder.String())
}

//初始化日志上报
func initLogger(fileName string, maxSize int, maxBackups int, maxAge int) *zap.Logger {
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
		Filename:   fileName,   // 输出文件
		MaxSize:    maxSize,    // 日志文件最大大小（单位：MB）
		MaxBackups: maxBackups, // 保留的旧日志文件最大数量
		MaxAge:     maxAge,     // 保存日期
	})
	core := zapcore.NewCore(zapcore.NewConsoleEncoder(encCfg), w, zap.InfoLevel)
	// 不打印路径
	return zap.New(core)
}
