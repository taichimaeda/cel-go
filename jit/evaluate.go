// Copyright 2026 Taichi Maeda
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package jit

import (
	"reflect"
	"runtime"
)

// TryEvaluate checks the dynamic type against the configured activation type,
// extracts the underlying struct pointer, and calls the native eval function.
// Returns (result, true) on success, or (false, false) if the input type does
// not match.
func TryEvaluate(eval NativeEvalFunc, activationType reflect.Type, input any) (bool, bool) {
	t := reflect.TypeOf(input)
	if t == nil || t != activationType {
		return false, false
	}
	result := eval(extractDataPointer(input))
	// Prevent the GC from collecting input before the native call completes.
	runtime.KeepAlive(input)
	return result, true
}
