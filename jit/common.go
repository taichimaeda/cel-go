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
	"fmt"
	"reflect"
	"unicode"

	celtypes "github.com/taichimaeda/cel-go/common/types"
)

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func toLowerFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	lowered := unicode.ToLower(runes[0])
	if lowered == runes[0] {
		return s
	}
	runes[0] = lowered
	return string(runes)
}

func celTypeToIRType(t *celtypes.Type) (Type, error) {
	switch t.Kind() {
	case celtypes.IntKind:
		return T_INT64, nil
	case celtypes.UintKind:
		return T_UINT64, nil
	case celtypes.DoubleKind:
		return T_FLOAT64, nil
	case celtypes.BoolKind:
		return T_BOOL, nil
	case celtypes.StringKind:
		return T_STRING, nil
	case celtypes.BytesKind:
		return T_UNSPECIFIED, fmt.Errorf("%w: CEL type bytes is not supported", ErrTranslateUnsupported)
	case celtypes.DynKind:
		return T_UNSPECIFIED, fmt.Errorf("%w: CEL type dyn is not supported", ErrTranslateUnsupported)
	default:
		return T_UNSPECIFIED, fmt.Errorf("%w: CEL type %v is not supported", ErrTranslateUnsupported, t.Kind())
	}
}

func reflectKindToIRType(kind reflect.Kind) (Type, error) {
	switch kind {
	case reflect.Int:
		// JIT backends are currently amd64/arm64 only (int is 64 bit).
		return T_INT64, nil
	case reflect.Uint:
		// JIT backends are currently amd64/arm64 only (uint is 64 bit).
		return T_UINT64, nil
	case reflect.Int64:
		return T_INT64, nil
	case reflect.Uint64:
		return T_UINT64, nil
	case reflect.Float64:
		return T_FLOAT64, nil
	case reflect.Bool:
		return T_BOOL, nil
	case reflect.String:
		return T_STRING, nil
	default:
		return T_UNSPECIFIED, fmt.Errorf("%w: reflect kind %v is not supported", ErrTranslateUnsupported, kind)
	}
}
