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
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestProbeSinkWriteAndSync(t *testing.T) {
	Convey("独立探测文件 Write+Sync", t, func() {
		dir := t.TempDir()
		path := filepath.Join(dir, "polaris-limiter-probe.log")
		sink := newProbeSink(Config{
			Path:               path,
			RotationMaxSize:    10,
			RotationMaxAge:     7,
			RotationMaxBackups: 3,
		})
		So(sink.WriteAndSync([]byte(probeLine)), ShouldBeNil)
		data, err := os.ReadFile(path)
		So(err, ShouldBeNil)
		So(string(data), ShouldContainSubstring, "iohang-probe")
	})
}
