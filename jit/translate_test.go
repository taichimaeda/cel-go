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

package jit_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/taichimaeda/cel-go/cel"
	celast "github.com/taichimaeda/cel-go/common/ast"
	"github.com/taichimaeda/cel-go/jit"
)

func TestTranslateStructFieldLoadsAndOps(t *testing.T) {
	type request struct {
		A int64
		B int64
	}
	env, err := cel.NewEnv(
		cel.Variable("b", cel.IntType),
		cel.Variable("a", cel.IntType),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, "b + a > 10")
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD)
	assertHasOpcode(t, prog, jit.ADD_INT)
	assertHasOpcode(t, prog, jit.GT_INT)
	assertHasOpcode(t, prog, jit.RETURN)
}

func TestTranslateStructFieldIntAndUintKinds(t *testing.T) {
	env, err := cel.NewEnv(
		cel.Variable("a", cel.IntType),
		cel.Variable("u", cel.UintType),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, "a > 0 && u > 0u")

	t.Run("BuiltinKinds", func(t *testing.T) {
		type request struct {
			A int
			U uint
		}
		prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
		if err != nil {
			t.Fatalf("Translate() failed: %v", err)
		}
		assertHasOpcode(t, prog, jit.GT_INT)
		assertHasOpcode(t, prog, jit.GT_UINT)
		assertHasOpcode(t, prog, jit.LOAD_FIELD)
	})

	t.Run("DefinedKinds", func(t *testing.T) {
		type myInt int
		type myUint uint
		type request struct {
			A myInt
			U myUint
		}
		prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
		if err != nil {
			t.Fatalf("Translate() failed: %v", err)
		}
		assertHasOpcode(t, prog, jit.GT_INT)
		assertHasOpcode(t, prog, jit.GT_UINT)
		assertHasOpcode(t, prog, jit.LOAD_FIELD)
	})
}

func TestTranslateLogicalAndLowering(t *testing.T) {
	type request struct {
		A bool
		B bool
	}
	env, err := cel.NewEnv(
		cel.Variable("a", cel.BoolType),
		cel.Variable("b", cel.BoolType),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, "a && b")
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	if countOpcode(prog, jit.BR_FALSE) != 2 {
		t.Fatalf("expected 2 BR_FALSE ops, got %d", countOpcode(prog, jit.BR_FALSE))
	}
	if countOpcode(prog, jit.CONST_BOOL) < 2 {
		t.Fatalf("expected at least 2 CONST_BOOL ops, got %d", countOpcode(prog, jit.CONST_BOOL))
	}
}

func TestTranslateBoolEqualsGenericOverload(t *testing.T) {
	type request struct {
		A bool
		B bool
	}
	env, err := cel.NewEnv(
		cel.Variable("a", cel.BoolType),
		cel.Variable("b", cel.BoolType),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, "a == b")
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.EQ_INT)
}

func TestTranslateStringStartsWithBuiltin(t *testing.T) {
	type request struct {
		S string
	}
	env, err := cel.NewEnv(cel.Variable("s", cel.StringType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `s.startsWith("ab")`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasBuiltinCall(t, prog, jit.BuiltinStrStarts)
}

func TestTranslateStringConcatEquals(t *testing.T) {
	type request struct {
		S string
	}
	env, err := cel.NewEnv(cel.Variable("s", cel.StringType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `(s + "xy") == "abxy"`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasBuiltinCall(t, prog, jit.BuiltinStrConcat)
	assertHasBuiltinCall(t, prog, jit.BuiltinStrEq)
}

func TestTranslateInStringListLiteral(t *testing.T) {
	type request struct {
		SeriesID string
	}
	env, err := cel.NewEnv(cel.Variable("seriesID", cel.StringType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `seriesID in ["35-53","87-1084"]`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	if got := countBuiltinCall(prog, jit.BuiltinStrEq); got != 2 {
		t.Fatalf("expected 2 BuiltinStrEq calls, got %d", got)
	}
}

func TestTranslateInIntListLiteral(t *testing.T) {
	type request struct {
		Value int64
	}
	env, err := cel.NewEnv(cel.Variable("value", cel.IntType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `value in [1, 2, 3, 5, 8]`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	if got := countOpcode(prog, jit.EQ_INT); got != 5 {
		t.Fatalf("expected 5 EQ_INT ops, got %d", got)
	}
}

func TestTranslateInUintListLiteral(t *testing.T) {
	type request struct {
		Value uint64
	}
	env, err := cel.NewEnv(cel.Variable("value", cel.UintType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `value in [1u, 2u, 3u]`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	if got := countOpcode(prog, jit.EQ_UINT); got != 3 {
		t.Fatalf("expected 3 EQ_UINT ops, got %d", got)
	}
}

func TestTranslateInFloatListLiteral(t *testing.T) {
	type request struct {
		Value float64
	}
	env, err := cel.NewEnv(cel.Variable("value", cel.DoubleType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `value in [1.0, 2.5, 3.14]`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	if got := countOpcode(prog, jit.EQ_FLOAT); got != 3 {
		t.Fatalf("expected 3 EQ_FLOAT ops, got %d", got)
	}
}

func TestTranslateInBoolListLiteral(t *testing.T) {
	type request struct {
		Value bool
	}
	env, err := cel.NewEnv(cel.Variable("value", cel.BoolType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `value in [true, false]`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	if got := countOpcode(prog, jit.EQ_INT); got != 2 {
		t.Fatalf("expected 2 EQ_INT ops (bool uses int comparison), got %d", got)
	}
}

func TestTranslateInStringListLiteralAtLimit(t *testing.T) {
	type request struct {
		SeriesID string
	}
	env, err := cel.NewEnv(cel.Variable("seriesID", cel.StringType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	lits := []string{
		`"a"`, `"b"`, `"c"`, `"d"`, `"e"`, `"f"`, `"g"`, `"h"`,
		`"i"`, `"j"`, `"k"`, `"l"`, `"m"`, `"n"`, `"o"`, `"p"`,
	}
	expr := "seriesID in [" + strings.Join(lits, ",") + "]"
	ast := mustCompileChecked(t, env, expr)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	if got := countOpcode(prog, jit.CALL_BUILTIN); got != len(lits) {
		t.Fatalf("expected %d CALL_BUILTIN ops, got %d", len(lits), got)
	}
}

func TestTranslateStructInStringSliceField(t *testing.T) {
	type request struct {
		Needle string
		TagIDs []string
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.StringType),
		cel.Variable("tagIDs", cel.ListType(cel.StringType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in tagIDs`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_SLICE)
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsStringSlice)
}

func TestTranslateStructInStringArrayField(t *testing.T) {
	type request struct {
		Needle string
		TagIDs [3]string
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.StringType),
		cel.Variable("tagIDs", cel.ListType(cel.StringType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in tagIDs`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_ARRAY)
	for _, ins := range prog.Instrs {
		if ins.Op == jit.CALL_BUILTIN && ins.BuiltinID == jit.BuiltinListContainsStringArray {
			if ins.Imm != 3 {
				t.Fatalf("expected array length imm=3, got %d", ins.Imm)
			}
		}
	}
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsStringArray)
}

func TestTranslateStructInIntSliceField(t *testing.T) {
	type request struct {
		Needle int64
		Values []int64
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.IntType),
		cel.Variable("values", cel.ListType(cel.IntType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_SLICE)
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsIntSlice)
}

func TestTranslateStructInIntArrayField(t *testing.T) {
	type request struct {
		Needle int64
		Values [5]int64
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.IntType),
		cel.Variable("values", cel.ListType(cel.IntType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_ARRAY)
	for _, ins := range prog.Instrs {
		if ins.Op == jit.CALL_BUILTIN && ins.BuiltinID == jit.BuiltinListContainsIntArray {
			if ins.Imm != 5 {
				t.Fatalf("expected array length imm=5, got %d", ins.Imm)
			}
		}
	}
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsIntArray)
}

func TestTranslateStructInUintSliceField(t *testing.T) {
	type request struct {
		Needle uint64
		Values []uint64
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.UintType),
		cel.Variable("values", cel.ListType(cel.UintType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_SLICE)
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsUintSlice)
}

func TestTranslateStructInUintArrayField(t *testing.T) {
	type request struct {
		Needle uint64
		Values [4]uint64
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.UintType),
		cel.Variable("values", cel.ListType(cel.UintType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_ARRAY)
	for _, ins := range prog.Instrs {
		if ins.Op == jit.CALL_BUILTIN && ins.BuiltinID == jit.BuiltinListContainsUintArray {
			if ins.Imm != 4 {
				t.Fatalf("expected array length imm=4, got %d", ins.Imm)
			}
		}
	}
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsUintArray)
}

func TestTranslateStructInFloatSliceField(t *testing.T) {
	type request struct {
		Needle float64
		Values []float64
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.DoubleType),
		cel.Variable("values", cel.ListType(cel.DoubleType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_SLICE)
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsFloatSlice)
}

func TestTranslateStructInFloatArrayField(t *testing.T) {
	type request struct {
		Needle float64
		Values [2]float64
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.DoubleType),
		cel.Variable("values", cel.ListType(cel.DoubleType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_ARRAY)
	for _, ins := range prog.Instrs {
		if ins.Op == jit.CALL_BUILTIN && ins.BuiltinID == jit.BuiltinListContainsFloatArray {
			if ins.Imm != 2 {
				t.Fatalf("expected array length imm=2, got %d", ins.Imm)
			}
		}
	}
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsFloatArray)
}

func TestTranslateStructInBoolSliceField(t *testing.T) {
	type request struct {
		Needle bool
		Values []bool
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.BoolType),
		cel.Variable("values", cel.ListType(cel.BoolType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_SLICE)
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsBoolSlice)
}

func TestTranslateStructInBoolArrayField(t *testing.T) {
	type request struct {
		Needle bool
		Values [3]bool
	}
	env, err := cel.NewEnv(
		cel.Variable("needle", cel.BoolType),
		cel.Variable("values", cel.ListType(cel.BoolType)),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `needle in values`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD_ARRAY)
	for _, ins := range prog.Instrs {
		if ins.Op == jit.CALL_BUILTIN && ins.BuiltinID == jit.BuiltinListContainsBoolArray {
			if ins.Imm != 3 {
				t.Fatalf("expected array length imm=3, got %d", ins.Imm)
			}
		}
	}
	assertHasBuiltinCall(t, prog, jit.BuiltinListContainsBoolArray)
}

func TestTranslateUnsupportedCases(t *testing.T) {
	t.Run("InStringListLiteralOverLimit", func(t *testing.T) {
		type request struct {
			SeriesID string
		}
		env, err := cel.NewEnv(cel.Variable("seriesID", cel.StringType))
		if err != nil {
			t.Fatalf("cel.NewEnv() failed: %v", err)
		}
		// Generate 1025 literals to exceed the limit of 1024
		lits := make([]string, 1025)
		for i := 0; i < 1025; i++ {
			lits[i] = fmt.Sprintf(`"elem%d"`, i)
		}
		src := "seriesID in [" + strings.Join(lits, ",") + "]"
		assertTranslateUnsupported(t, env, src, reflect.TypeOf(&request{}))
	})

	t.Run("InStringListIdentNonStringField", func(t *testing.T) {
		type request struct {
			Needle string
			TagIDs []int64
		}
		env, err := cel.NewEnv(
			cel.Variable("needle", cel.StringType),
			cel.Variable("tagIDs", cel.ListType(cel.StringType)),
		)
		if err != nil {
			t.Fatalf("cel.NewEnv() failed: %v", err)
		}
		assertTranslateUnsupported(t, env, `needle in tagIDs`, reflect.TypeOf(&request{}))
	})

	t.Run("InStringListNonIdentRHS", func(t *testing.T) {
		type request struct {
			Needle string
			TagIDs []string
			UseTag bool
		}
		env, err := cel.NewEnv(
			cel.Variable("needle", cel.StringType),
			cel.Variable("tagIDs", cel.ListType(cel.StringType)),
			cel.Variable("useTag", cel.BoolType),
		)
		if err != nil {
			t.Fatalf("cel.NewEnv() failed: %v", err)
		}
		assertTranslateUnsupported(t, env, `needle in (useTag ? tagIDs : tagIDs)`, reflect.TypeOf(&request{}))
	})

	t.Run("StructRequiresBoolResult", func(t *testing.T) {
		type request struct {
			A int64
		}
		env, err := cel.NewEnv(cel.Variable("a", cel.IntType))
		if err != nil {
			t.Fatalf("cel.NewEnv() failed: %v", err)
		}
		assertTranslateUnsupported(t, env, "a + 1", reflect.TypeOf(&request{}))
	})

	t.Run("UnsupportedListLiteral", func(t *testing.T) {
		type request struct {
			A int64
		}
		env, err := cel.NewEnv()
		if err != nil {
			t.Fatalf("cel.NewEnv() failed: %v", err)
		}
		assertTranslateUnsupported(t, env, "[1, 2, 3]", reflect.TypeOf(&request{}))
	})
}

func TestTranslateStructFieldAliasCollision(t *testing.T) {
	type request struct {
		X string
		Y string `json:"x"`
	}
	env, err := cel.NewEnv(cel.Variable("x", cel.StringType))
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `x == "ok"`)
	_, err = jit.Translate(ast, reflect.TypeOf(&request{}))
	if err == nil || !strings.Contains(err.Error(), "ambiguous field aliases") {
		t.Fatalf("expected ambiguous field alias error, got %v", err)
	}
}

func TestTranslateStructFieldProtobufTag(t *testing.T) {
	type request struct {
		UserID   string `protobuf:"bytes,1,opt,name=user_id,proto3"`
		FullName string `protobuf:"bytes,2,opt,name=full_name,proto3"`
	}
	env, err := cel.NewEnv(
		cel.Variable("user_id", cel.StringType),
		cel.Variable("full_name", cel.StringType),
	)
	if err != nil {
		t.Fatalf("cel.NewEnv() failed: %v", err)
	}
	ast := mustCompileChecked(t, env, `user_id == "alice" && full_name == "Alice Smith"`)
	prog, err := jit.Translate(ast, reflect.TypeOf(&request{}))
	if err != nil {
		t.Fatalf("Translate() failed: %v", err)
	}
	assertHasOpcode(t, prog, jit.LOAD_FIELD)
	assertHasOpcode(t, prog, jit.RETURN)
}

func assertTranslateUnsupported(t *testing.T, env *cel.Env, src string, structType reflect.Type) {
	t.Helper()
	ast := mustCompileChecked(t, env, src)
	_, err := jit.Translate(ast, structType)
	if !errors.Is(err, jit.ErrTranslateUnsupported) {
		t.Fatalf("expected ErrTranslateUnsupported, got %v", err)
	}
}

func countBuiltinCall(p *jit.Program, id jit.BuiltinID) int {
	count := 0
	for _, ins := range p.Instrs {
		if ins.Op == jit.CALL_BUILTIN && ins.BuiltinID == id {
			count++
		}
	}
	return count
}

func assertHasBuiltinCall(t *testing.T, p *jit.Program, id jit.BuiltinID) {
	t.Helper()
	if countBuiltinCall(p, id) == 0 {
		t.Fatalf("missing builtin call %v in instructions: %#v", id, p.Instrs)
	}
}

func mustCompileChecked(t *testing.T, env *cel.Env, src string) *celast.AST {
	t.Helper()
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("env.Compile(%q) failed: %v", src, iss.Err())
	}
	if ast == nil {
		t.Fatalf("env.Compile(%q) returned nil AST", src)
	}
	return ast.NativeRep()
}

func countOpcode(p *jit.Program, op jit.Opcode) int {
	count := 0
	for _, ins := range p.Instrs {
		if ins.Op == op {
			count++
		}
	}
	return count
}

func assertHasOpcode(t *testing.T, p *jit.Program, op jit.Opcode) {
	t.Helper()
	if countOpcode(p, op) == 0 {
		t.Fatalf("missing opcode %v in instructions: %#v", op, p.Instrs)
	}
}
