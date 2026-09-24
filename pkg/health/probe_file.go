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

import (
	"fmt"
	"os"

	"github.com/natefinch/lumberjack"
)

// Writer 探测落盘接口，便于单测替换。
type Writer interface {
	WriteAndSync(p []byte) error
}

// probeSink 独立探测文件：lumberjack 负责滚动，Write 之后对当前文件 fsync。
type probeSink struct {
	path string
	lj   *lumberjack.Logger
}

func newProbeSink(cfg Config) *probeSink {
	return &probeSink{
		path: cfg.Path,
		lj: &lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    cfg.RotationMaxSize,
			MaxAge:     cfg.RotationMaxAge,
			MaxBackups: cfg.RotationMaxBackups,
		},
	}
}

// WriteAndSync 写入探测行并 fsync。lumberjack 本身不 Sync，需再打开当前路径做一次。
func (s *probeSink) WriteAndSync(p []byte) error {
	if _, err := s.lj.Write(p); err != nil {
		return fmt.Errorf("write probe file %s: %w", s.path, err)
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open probe file for sync %s: %w", s.path, err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync probe file %s: %w", s.path, err)
	}
	return nil
}
