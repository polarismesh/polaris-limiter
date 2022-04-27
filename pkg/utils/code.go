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

// 转为http status
func code2CommonCode(code uint32) int {
	value := int(code / 1000)
	if value < 100 {
		return 0
	}
	return (value / 100) * 100

}

// IsSuccess 是否成功错误码
func IsSuccess(code uint32) bool {
	return code2CommonCode(code) == 200
}

// IsUserErr 是否成功错误码
func IsUserErr(code uint32) bool {
	return code2CommonCode(code) == 400
}

// IsSysErr 是否成功错误码
func IsSysErr(code uint32) bool {
	return code2CommonCode(code) == 500
}
