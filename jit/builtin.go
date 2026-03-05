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
	"strings"
	"unicode/utf8"
	"unsafe"
)

func helperStrEq(a, b string) bool {
	return a == b
}

func helperStrNe(a, b string) bool {
	return a != b
}

func helperStrContains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func helperStrStarts(s, prefix string) bool {
	return strings.HasPrefix(s, prefix)
}

func helperStrEnds(s, suffix string) bool {
	return strings.HasSuffix(s, suffix)
}

func helperStrConcat(a, b string) string {
	return a + b
}

func helperStrSize(s string) int64 {
	return int64(utf8.RuneCountInString(s))
}

func helperListContainsStringSlice(needle string, sliceAddr unsafe.Pointer) bool {
	if sliceAddr == nil {
		return false
	}
	// sliceAddr points at a []string field in the input struct. The field address
	// is stable during evaluation because the input object is kept alive until the
	// native call returns.
	items := *(*[]string)(sliceAddr)
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsStringArray(needle string, arrayAddr unsafe.Pointer, n int64) bool {
	if arrayAddr == nil || n <= 0 {
		return false
	}
	items := unsafe.Slice((*string)(arrayAddr), int(n))
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

var builtinFunctionValues = [...]uintptr{
	BuiltinStrEq:                   extractDataUintptr(helperStrEq),
	BuiltinStrNe:                   extractDataUintptr(helperStrNe),
	BuiltinStrContains:             extractDataUintptr(helperStrContains),
	BuiltinStrStarts:               extractDataUintptr(helperStrStarts),
	BuiltinStrEnds:                 extractDataUintptr(helperStrEnds),
	BuiltinStrConcat:               extractDataUintptr(helperStrConcat),
	BuiltinStrSize:                 extractDataUintptr(helperStrSize),
	BuiltinListContainsStringSlice: extractDataUintptr(helperListContainsStringSlice),
	BuiltinListContainsStringArray: extractDataUintptr(helperListContainsStringArray),
}

func builtinFunctionValue(id BuiltinID) (uintptr, bool) {
	if int(id) >= len(builtinFunctionValues) {
		return 0, false
	}
	fn := builtinFunctionValues[id]
	return fn, fn != 0
}
