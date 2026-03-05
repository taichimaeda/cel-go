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

func helperListContainsIntSlice(needle int64, sliceAddr unsafe.Pointer) bool {
	if sliceAddr == nil {
		return false
	}
	items := *(*[]int64)(sliceAddr)
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsIntArray(needle int64, arrayAddr unsafe.Pointer, n int64) bool {
	if arrayAddr == nil || n <= 0 {
		return false
	}
	items := unsafe.Slice((*int64)(arrayAddr), int(n))
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsUintSlice(needle uint64, sliceAddr unsafe.Pointer) bool {
	if sliceAddr == nil {
		return false
	}
	items := *(*[]uint64)(sliceAddr)
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsUintArray(needle uint64, arrayAddr unsafe.Pointer, n int64) bool {
	if arrayAddr == nil || n <= 0 {
		return false
	}
	items := unsafe.Slice((*uint64)(arrayAddr), int(n))
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsFloatSlice(needle float64, sliceAddr unsafe.Pointer) bool {
	if sliceAddr == nil {
		return false
	}
	items := *(*[]float64)(sliceAddr)
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsFloatArray(needle float64, arrayAddr unsafe.Pointer, n int64) bool {
	if arrayAddr == nil || n <= 0 {
		return false
	}
	items := unsafe.Slice((*float64)(arrayAddr), int(n))
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsBoolSlice(needle bool, sliceAddr unsafe.Pointer) bool {
	if sliceAddr == nil {
		return false
	}
	items := *(*[]bool)(sliceAddr)
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

func helperListContainsBoolArray(needle bool, arrayAddr unsafe.Pointer, n int64) bool {
	if arrayAddr == nil || n <= 0 {
		return false
	}
	items := unsafe.Slice((*bool)(arrayAddr), int(n))
	for _, v := range items {
		if v == needle {
			return true
		}
	}
	return false
}

var builtinFunctions = [...]uintptr{
	BuiltinStrEq:                   extractDataUintptr(helperStrEq),
	BuiltinStrNe:                   extractDataUintptr(helperStrNe),
	BuiltinStrContains:             extractDataUintptr(helperStrContains),
	BuiltinStrStarts:               extractDataUintptr(helperStrStarts),
	BuiltinStrEnds:                 extractDataUintptr(helperStrEnds),
	BuiltinStrConcat:               extractDataUintptr(helperStrConcat),
	BuiltinStrSize:                 extractDataUintptr(helperStrSize),
	BuiltinListContainsStringSlice: extractDataUintptr(helperListContainsStringSlice),
	BuiltinListContainsStringArray: extractDataUintptr(helperListContainsStringArray),
	BuiltinListContainsIntSlice:    extractDataUintptr(helperListContainsIntSlice),
	BuiltinListContainsIntArray:    extractDataUintptr(helperListContainsIntArray),
	BuiltinListContainsUintSlice:   extractDataUintptr(helperListContainsUintSlice),
	BuiltinListContainsUintArray:   extractDataUintptr(helperListContainsUintArray),
	BuiltinListContainsFloatSlice:  extractDataUintptr(helperListContainsFloatSlice),
	BuiltinListContainsFloatArray:  extractDataUintptr(helperListContainsFloatArray),
	BuiltinListContainsBoolSlice:   extractDataUintptr(helperListContainsBoolSlice),
	BuiltinListContainsBoolArray:   extractDataUintptr(helperListContainsBoolArray),
}

func builtinFunction(id BuiltinID) (uintptr, bool) {
	if int(id) >= len(builtinFunctions) {
		return 0, false
	}
	fn := builtinFunctions[id]
	return fn, fn != 0
}
