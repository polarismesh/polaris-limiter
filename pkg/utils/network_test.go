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

import (
	"fmt"
	"net"
	"testing"
)

func TestNewIPAddress(t *testing.T) {
	ipStr := "127.0.1.115"
	ipAddr := NewIPAddress(ipStr)
	fmt.Printf("transformed ip is %s\n", *ipAddr)

	ipv6Str := "2001:0db8:3c4d:0015:0110:0020:1a2f:1a2b"

	normalIP := net.ParseIP(ipv6Str)
	fmt.Printf("normal ip is %s\n", normalIP)
	ipAddr = NewIPAddress(ipv6Str)
	fmt.Printf("transformed ip is %s\n", *ipAddr)
}
