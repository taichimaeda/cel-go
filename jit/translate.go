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
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

type overloadSpec struct {
	op      Opcode
	builtin BuiltinID
	arity   int
	result  Type
}

var overloadTable = map[string]overloadSpec{
	"add_int64":          {op: ADD_INT, arity: 2, result: T_INT64},
	"add_uint64":         {op: ADD_UINT, arity: 2, result: T_UINT64},
	"add_double":         {op: ADD_FLOAT, arity: 2, result: T_FLOAT64},
	"add_string":         {op: CALL_BUILTIN, builtin: BuiltinStrConcat, arity: 2, result: T_STRING},
	"subtract_int64":     {op: SUB_INT, arity: 2, result: T_INT64},
	"subtract_uint64":    {op: SUB_UINT, arity: 2, result: T_UINT64},
	"subtract_double":    {op: SUB_FLOAT, arity: 2, result: T_FLOAT64},
	"multiply_int64":     {op: MUL_INT, arity: 2, result: T_INT64},
	"multiply_uint64":    {op: MUL_UINT, arity: 2, result: T_UINT64},
	"multiply_double":    {op: MUL_FLOAT, arity: 2, result: T_FLOAT64},
	"divide_int64":       {op: DIV_INT, arity: 2, result: T_INT64},
	"divide_uint64":      {op: DIV_UINT, arity: 2, result: T_UINT64},
	"divide_double":      {op: DIV_FLOAT, arity: 2, result: T_FLOAT64},
	"modulo_int64":       {op: MOD_INT, arity: 2, result: T_INT64},
	"modulo_uint64":      {op: MOD_UINT, arity: 2, result: T_UINT64},
	"negate_int64":       {op: NEG_INT, arity: 1, result: T_INT64},
	"negate_double":      {op: NEG_FLOAT, arity: 1, result: T_FLOAT64},
	"less_int64":         {op: LT_INT, arity: 2, result: T_BOOL},
	"less_uint64":        {op: LT_UINT, arity: 2, result: T_BOOL},
	"less_double":        {op: LT_FLOAT, arity: 2, result: T_BOOL},
	"less_equals_int64":  {op: LE_INT, arity: 2, result: T_BOOL},
	"less_equals_uint64": {op: LE_UINT, arity: 2, result: T_BOOL},
	"less_equals_double": {op: LE_FLOAT, arity: 2, result: T_BOOL},
	"greater_int64":      {op: GT_INT, arity: 2, result: T_BOOL},
	"greater_uint64":     {op: GT_UINT, arity: 2, result: T_BOOL},
	"greater_double":     {op: GT_FLOAT, arity: 2, result: T_BOOL},
	"greater_equals_int64": {
		op: GE_INT, arity: 2, result: T_BOOL,
	},
	"greater_equals_uint64": {
		op: GE_UINT, arity: 2, result: T_BOOL,
	},
	"greater_equals_double": {
		op: GE_FLOAT, arity: 2, result: T_BOOL,
	},
	"equals_int64":      {op: EQ_INT, arity: 2, result: T_BOOL},
	"equals_uint64":     {op: EQ_UINT, arity: 2, result: T_BOOL},
	"equals_double":     {op: EQ_FLOAT, arity: 2, result: T_BOOL},
	"not_equals_int64":  {op: NE_INT, arity: 2, result: T_BOOL},
	"not_equals_uint64": {op: NE_UINT, arity: 2, result: T_BOOL},
	"not_equals_double": {op: NE_FLOAT, arity: 2, result: T_BOOL},
	"equals_string":     {op: CALL_BUILTIN, builtin: BuiltinStrEq, arity: 2, result: T_BOOL},
	"not_equals_string": {op: CALL_BUILTIN, builtin: BuiltinStrNe, arity: 2, result: T_BOOL},
	"contains_string":   {op: CALL_BUILTIN, builtin: BuiltinStrContains, arity: 2, result: T_BOOL},
	"starts_with_string": {
		op: CALL_BUILTIN, builtin: BuiltinStrStarts, arity: 2, result: T_BOOL,
	},
	"ends_with_string": {op: CALL_BUILTIN, builtin: BuiltinStrEnds, arity: 2, result: T_BOOL},
	"size_string":      {op: CALL_BUILTIN, builtin: BuiltinStrSize, arity: 1, result: T_INT64},
	"logical_not":      {op: NOT, arity: 1, result: T_BOOL},
}

// Translate turns a checked AST into an IR program that loads variables
// directly from a concrete pointer-to-struct input.
//
// Constraints:
//   - expression result type must be bool
//   - struct field types must exactly match CEL variable types
//   - supported scalar field kinds: int64/int, uint64/uint, float64, bool, string
//   - supported list membership field kinds for in_list: []string, [N]string
func Translate(ast *celast.AST, activationType reflect.Type) (*Program, error) {
	if ast == nil || !ast.IsChecked() {
		return nil, fmt.Errorf("%w: AST must be non-nil and checked", ErrTranslateUnsupported)
	}
	if activationType == nil || activationType.Kind() != reflect.Ptr || activationType.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: activation type must be pointer to struct, got %v", ErrTranslateUnsupported, activationType)
	}
	if ast.GetType(ast.Expr().ID()).Kind() != celtypes.BoolKind {
		return nil, fmt.Errorf("%w: root expression must evaluate to bool", ErrTranslateUnsupported)
	}
	tr := &translator{
		ast:        ast,
		instrs:     make([]Instr, 0, 64),
		nextVReg:   1,
		nextLabel:  1,
		stringIdx:  make(map[string]int),
		structType: activationType.Elem(),
	}
	fieldMap, err := tr.resolveStructFieldIndex()
	if err != nil {
		return nil, err
	}
	tr.structFieldMap = fieldMap
	root, rootType, err := tr.translateExpr(ast.Expr())
	if err != nil {
		return nil, err
	}
	if rootType != T_BOOL {
		return nil, fmt.Errorf("%w: translated root type must be bool, got %v", ErrTranslateUnsupported, rootType)
	}
	tr.emitInstr(Instr{Op: RETURN, Src1: root, Type: T_BOOL})
	if err := tr.resolveLabels(); err != nil {
		return nil, err
	}
	return &Program{
		Instrs:     tr.instrs,
		NumVRegs:   int(tr.nextVReg),
		StringPool: tr.stringPool,
	}, nil
}

type translator struct {
	ast        *celast.AST
	instrs     []Instr
	nextVReg   VReg
	nextLabel  int
	stringPool []string
	stringIdx  map[string]int

	structType     reflect.Type
	structFieldMap map[string][]int
}

func (t *translator) emitInstr(ins Instr) {
	t.instrs = append(t.instrs, ins)
}

func (t *translator) emitLabel(lbl Label) {
	t.emitInstr(Instr{Op: LABEL, Lbl: lbl})
}

func (t *translator) internString(v string) int {
	if idx, found := t.stringIdx[v]; found {
		return idx
	}
	idx := len(t.stringPool)
	t.stringPool = append(t.stringPool, v)
	t.stringIdx[v] = idx
	return idx
}

func (t *translator) translateExpr(expr celast.Expr) (VReg, Type, error) {
	switch expr.Kind() {
	case celast.LiteralKind:
		return t.translateLiteral(expr.AsLiteral())
	case celast.IdentKind:
		return t.translateIdent(expr)
	case celast.CallKind:
		return t.translateCall(expr)
	case celast.ComprehensionKind, celast.ListKind, celast.MapKind, celast.StructKind, celast.SelectKind:
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: expression kind %v is unsupported", ErrTranslateUnsupported, expr.Kind())
	default:
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: unknown expression kind %v", ErrTranslateUnsupported, expr.Kind())
	}
}

func (t *translator) translateLiteral(v ref.Val) (VReg, Type, error) {
	if v == nil {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: nil literal value", ErrTranslateUnsupported)
	}
	dst := t.newVReg()
	switch c := v.Value().(type) {
	case int64:
		t.emitInstr(Instr{Op: CONST_INT, Dst: dst, Imm: c, Type: T_INT64})
		return dst, T_INT64, nil
	case uint64:
		t.emitInstr(Instr{Op: CONST_UINT, Dst: dst, Imm: int64(c), Type: T_UINT64})
		return dst, T_UINT64, nil
	case float64:
		t.emitInstr(Instr{Op: CONST_FLOAT, Dst: dst, Imm: int64(math.Float64bits(c)), Type: T_FLOAT64})
		return dst, T_FLOAT64, nil
	case bool:
		t.emitInstr(Instr{Op: CONST_BOOL, Dst: dst, Imm: boolToInt(c), Type: T_BOOL})
		return dst, T_BOOL, nil
	case string:
		t.emitInstr(Instr{
			Op:   CONST_STRING,
			Dst:  dst,
			Imm:  int64(t.internString(c)),
			Type: T_STRING,
		})
		return dst, T_STRING, nil
	default:
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: literal type %T is unsupported", ErrTranslateUnsupported, c)
	}
}

func (t *translator) translateIdent(expr celast.Expr) (VReg, Type, error) {
	idRef, found := t.ast.ReferenceMap()[expr.ID()]
	if found && idRef != nil && idRef.Value != nil {
		return t.translateLiteral(idRef.Value)
	}

	idName := expr.AsIdent()
	fieldIndex, found := t.structFieldMap[idName]
	if !found {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: struct field for identifier %q was not found", ErrTranslateUnsupported, idName)
	}
	field := t.structType.FieldByIndex(fieldIndex)
	fieldType, err := reflectKindToIRType(field.Type.Kind())
	if err != nil {
		return 0, T_UNSPECIFIED, err
	}
	targetType, err := celTypeToIRType(t.ast.GetType(expr.ID()))
	if err != nil {
		return 0, T_UNSPECIFIED, err
	}
	if fieldType != targetType {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: identifier %q type mismatch (field kind=%v, expected CEL type=%v)", ErrTranslateUnsupported, idName, field.Type.Kind(), targetType)
	}

	dst := t.newVReg()
	t.emitInstr(Instr{
		Op:   LOAD_FIELD,
		Dst:  dst,
		Imm:  int64(field.Offset),
		Type: targetType,
	})
	return dst, targetType, nil
}

func (t *translator) translateCall(expr celast.Expr) (VReg, Type, error) {
	call := expr.AsCall()
	fn := call.FunctionName()
	switch fn {
	case operators.LogicalAnd:
		return t.translateLogicalAnd(call.Args())
	case operators.LogicalOr:
		return t.translateLogicalOr(call.Args())
	case operators.Conditional:
		return t.translateConditional(call.Args())
	case operators.In, operators.OldIn:
		return t.translateInList(expr, call.Args())
	}

	args := make([]celast.Expr, 0, len(call.Args())+1)
	if call.IsMemberFunction() {
		args = append(args, call.Target())
	}
	args = append(args, call.Args()...)

	argRegs := make([]VReg, 0, len(args))
	argTypes := make([]Type, 0, len(args))
	for _, arg := range args {
		r, typ, err := t.translateExpr(arg)
		if err != nil {
			return 0, T_UNSPECIFIED, err
		}
		argRegs = append(argRegs, r)
		argTypes = append(argTypes, typ)
	}

	overloads := t.ast.GetOverloadIDs(expr.ID())
	if len(overloads) == 0 {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: no overload IDs for call %q", ErrTranslateUnsupported, fn)
	}
	spec, found := overloadTable[overloads[0]]
	if !found {
		var ok bool
		spec, ok = t.resolveGenericOverload(overloads[0], argTypes)
		if !ok {
			return 0, T_UNSPECIFIED, fmt.Errorf("%w: overload %q with arg types %v is unsupported", ErrTranslateUnsupported, overloads[0], argTypes)
		}
	}
	if len(argRegs) < spec.arity {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: call %q arity mismatch (have %d, need %d)", ErrTranslateUnsupported, fn, len(argRegs), spec.arity)
	}

	dst := t.newVReg()
	ins := Instr{
		Op:   spec.op,
		Dst:  dst,
		Type: spec.result,
	}
	if spec.arity >= 1 {
		ins.Src1 = argRegs[0]
	}
	if spec.arity >= 2 {
		ins.Src2 = argRegs[1]
	}
	if spec.op == CALL_BUILTIN {
		ins.BuiltinID = spec.builtin
	}
	t.emitInstr(ins)
	return dst, spec.result, nil
}

func (t *translator) translateInList(expr celast.Expr, args []celast.Expr) (VReg, Type, error) {
	if len(args) != 2 {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: in_list expects exactly 2 arguments", ErrTranslateUnsupported)
	}
	overloads := t.ast.GetOverloadIDs(expr.ID())
	if len(overloads) == 0 || overloads[0] != "in_list" {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: in_list overload not found", ErrTranslateUnsupported)
	}
	needle, needleType, err := t.translateExpr(args[0])
	if err != nil {
		return 0, T_UNSPECIFIED, err
	}
	if needleType != T_STRING {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: in_list needle must be string, got %v", ErrTranslateUnsupported, needleType)
	}
	switch rhs := args[1]; rhs.Kind() {
	case celast.ListKind:
		return t.translateInStringListLiteral(needle, rhs.AsList().Elements())
	case celast.IdentKind:
		return t.translateInStringListIdent(needle, rhs.AsIdent())
	default:
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: in_list RHS kind %v is unsupported", ErrTranslateUnsupported, rhs.Kind())
	}
}

func (t *translator) translateInStringListLiteral(needle VReg, elems []celast.Expr) (VReg, Type, error) {
	const maxElems = 1024
	if len(elems) > maxElems {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: in_list literal size %d exceeds max %d", ErrTranslateUnsupported, len(elems), maxElems)
	}
	result := t.newVReg()
	if len(elems) == 0 {
		t.emitInstr(Instr{Op: CONST_BOOL, Dst: result, Imm: 0, Type: T_BOOL})
		return result, T_BOOL, nil
	}
	matchLbl := t.newLabel()
	endLbl := t.newLabel()
	for _, elem := range elems {
		if elem.Kind() != celast.LiteralKind {
			return 0, T_UNSPECIFIED, fmt.Errorf("%w: in_list literal elements must be literals", ErrTranslateUnsupported)
		}
		s, ok := elem.AsLiteral().Value().(string)
		if !ok {
			return 0, T_UNSPECIFIED, fmt.Errorf("%w: in_list literal elements must be strings", ErrTranslateUnsupported)
		}
		lit := t.newVReg()
		t.emitInstr(Instr{
			Op:   CONST_STRING,
			Dst:  lit,
			Imm:  int64(t.internString(s)),
			Type: T_STRING,
		})
		cmp := t.newVReg()
		t.emitInstr(Instr{
			Op:        CALL_BUILTIN,
			Dst:       cmp,
			Src1:      needle,
			Src2:      lit,
			Type:      T_BOOL,
			BuiltinID: BuiltinStrEq,
		})
		t.emitInstr(Instr{Op: BR_TRUE, Src1: cmp, Lbl: matchLbl})
	}
	t.emitInstr(Instr{Op: CONST_BOOL, Dst: result, Imm: 0, Type: T_BOOL})
	t.emitInstr(Instr{Op: BR, Lbl: endLbl})
	t.emitLabel(matchLbl)
	t.emitInstr(Instr{Op: CONST_BOOL, Dst: result, Imm: 1, Type: T_BOOL})
	t.emitLabel(endLbl)
	return result, T_BOOL, nil
}

func (t *translator) translateInStringListIdent(needle VReg, identName string) (VReg, Type, error) {
	field, found := t.resolveStructField(identName)
	if !found {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: list field %q was not found", ErrTranslateUnsupported, identName)
	}
	ptr := t.newVReg()
	switch field.Type.Kind() {
	case reflect.Slice:
		if field.Type.Elem().Kind() != reflect.String {
			return 0, T_UNSPECIFIED, fmt.Errorf("%w: list field %q must be []string, got []%v", ErrTranslateUnsupported, identName, field.Type.Elem().Kind())
		}
		t.emitInstr(Instr{
			Op:   LOAD_FIELD_SLICE,
			Dst:  ptr,
			Imm:  int64(field.Offset),
			Type: T_UINT64,
		})
		dst := t.newVReg()
		t.emitInstr(Instr{
			Op:        CALL_BUILTIN,
			Dst:       dst,
			Src1:      needle,
			Src2:      ptr,
			Type:      T_BOOL,
			BuiltinID: BuiltinListContainsStringSlice,
		})
		return dst, T_BOOL, nil
	case reflect.Array:
		if field.Type.Elem().Kind() != reflect.String {
			return 0, T_UNSPECIFIED, fmt.Errorf("%w: list field %q must be [N]string, got [%d]%v", ErrTranslateUnsupported, identName, field.Type.Len(), field.Type.Elem().Kind())
		}
		t.emitInstr(Instr{
			Op:   LOAD_FIELD_ARRAY,
			Dst:  ptr,
			Imm:  int64(field.Offset),
			Type: T_UINT64,
		})
		dst := t.newVReg()
		t.emitInstr(Instr{
			Op:        CALL_BUILTIN,
			Dst:       dst,
			Src1:      needle,
			Src2:      ptr,
			Imm:       int64(field.Type.Len()),
			Type:      T_BOOL,
			BuiltinID: BuiltinListContainsStringArray,
		})
		return dst, T_BOOL, nil
	default:
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: list field %q kind %v is unsupported", ErrTranslateUnsupported, identName, field.Type.Kind())
	}
}

func (t *translator) translateLogicalAnd(args []celast.Expr) (VReg, Type, error) {
	if len(args) < 2 {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: logical and requires at least 2 arguments", ErrTranslateUnsupported)
	}
	falseLbl := t.newLabel()
	endLbl := t.newLabel()
	for _, arg := range args {
		r, typ, err := t.translateExpr(arg)
		if err != nil {
			return 0, T_UNSPECIFIED, err
		}
		if typ != T_BOOL {
			return 0, T_UNSPECIFIED, fmt.Errorf("%w: logical and arguments must be bool, got %v", ErrTranslateUnsupported, typ)
		}
		t.emitInstr(Instr{Op: BR_FALSE, Src1: r, Lbl: falseLbl})
	}
	dst := t.newVReg()
	t.emitInstr(Instr{Op: CONST_BOOL, Dst: dst, Imm: 1, Type: T_BOOL})
	t.emitInstr(Instr{Op: BR, Lbl: endLbl})
	t.emitLabel(falseLbl)
	t.emitInstr(Instr{Op: CONST_BOOL, Dst: dst, Imm: 0, Type: T_BOOL})
	t.emitLabel(endLbl)
	return dst, T_BOOL, nil
}

func (t *translator) translateLogicalOr(args []celast.Expr) (VReg, Type, error) {
	if len(args) < 2 {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: logical or requires at least 2 arguments", ErrTranslateUnsupported)
	}
	trueLbl := t.newLabel()
	endLbl := t.newLabel()
	for _, arg := range args {
		r, typ, err := t.translateExpr(arg)
		if err != nil {
			return 0, T_UNSPECIFIED, err
		}
		if typ != T_BOOL {
			return 0, T_UNSPECIFIED, fmt.Errorf("%w: logical or arguments must be bool, got %v", ErrTranslateUnsupported, typ)
		}
		t.emitInstr(Instr{Op: BR_TRUE, Src1: r, Lbl: trueLbl})
	}
	dst := t.newVReg()
	t.emitInstr(Instr{Op: CONST_BOOL, Dst: dst, Imm: 0, Type: T_BOOL})
	t.emitInstr(Instr{Op: BR, Lbl: endLbl})
	t.emitLabel(trueLbl)
	t.emitInstr(Instr{Op: CONST_BOOL, Dst: dst, Imm: 1, Type: T_BOOL})
	t.emitLabel(endLbl)
	return dst, T_BOOL, nil
}

func (t *translator) translateConditional(args []celast.Expr) (VReg, Type, error) {
	if len(args) != 3 {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: conditional requires exactly 3 arguments", ErrTranslateUnsupported)
	}
	cond, typ, err := t.translateExpr(args[0])
	if err != nil {
		return 0, T_UNSPECIFIED, err
	}
	if typ != T_BOOL {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: conditional condition must be bool, got %v", ErrTranslateUnsupported, typ)
	}
	elseLbl := t.newLabel()
	endLbl := t.newLabel()
	t.emitInstr(Instr{Op: BR_FALSE, Src1: cond, Lbl: elseLbl})

	thenV, thenType, err := t.translateExpr(args[1])
	if err != nil {
		return 0, T_UNSPECIFIED, err
	}
	dst := t.newVReg()
	t.emitInstr(Instr{Op: MOVE, Dst: dst, Src1: thenV, Type: thenType})
	t.emitInstr(Instr{Op: BR, Lbl: endLbl})

	t.emitLabel(elseLbl)
	elseV, elseType, err := t.translateExpr(args[2])
	if err != nil {
		return 0, T_UNSPECIFIED, err
	}
	if thenType != elseType {
		return 0, T_UNSPECIFIED, fmt.Errorf("%w: conditional branch types must match (then=%v, else=%v)", ErrTranslateUnsupported, thenType, elseType)
	}
	t.emitInstr(Instr{Op: MOVE, Dst: dst, Src1: elseV, Type: thenType})
	t.emitLabel(endLbl)
	return dst, thenType, nil
}

func (t *translator) newVReg() VReg {
	r := t.nextVReg
	t.nextVReg++
	return r
}

func (t *translator) newLabel() Label {
	l := Label(t.nextLabel)
	t.nextLabel++
	return l
}

func (t *translator) resolveLabels() error {
	targets := make(map[Label]int)
	for i, ins := range t.instrs {
		if ins.Op == LABEL {
			targets[ins.Lbl] = i
		}
	}
	for i, ins := range t.instrs {
		switch ins.Op {
		case BR, BR_TRUE, BR_FALSE:
			target, found := targets[ins.Lbl]
			if !found {
				return fmt.Errorf("unresolved label: %d", ins.Lbl)
			}
			t.instrs[i].Lbl = Label(target)
		}
	}
	return nil
}

func (t *translator) resolveStructField(name string) (reflect.StructField, bool) {
	idx, found := t.structFieldMap[name]
	if !found {
		return reflect.StructField{}, false
	}
	return t.structType.FieldByIndex(idx), true
}

func (*translator) resolveGenericOverload(overload string, args []Type) (overloadSpec, bool) {
	if len(args) != 2 {
		return overloadSpec{}, false
	}
	lhs, rhs := args[0], args[1]
	if lhs != rhs {
		return overloadSpec{}, false
	}
	switch overload {
	case "equals":
		switch lhs {
		case T_INT64:
			return overloadSpec{op: EQ_INT, arity: 2, result: T_BOOL}, true
		case T_UINT64:
			return overloadSpec{op: EQ_UINT, arity: 2, result: T_BOOL}, true
		case T_FLOAT64:
			return overloadSpec{op: EQ_FLOAT, arity: 2, result: T_BOOL}, true
		case T_BOOL:
			return overloadSpec{op: EQ_INT, arity: 2, result: T_BOOL}, true
		case T_STRING:
			return overloadSpec{op: CALL_BUILTIN, builtin: BuiltinStrEq, arity: 2, result: T_BOOL}, true
		}
	case "not_equals":
		switch lhs {
		case T_INT64:
			return overloadSpec{op: NE_INT, arity: 2, result: T_BOOL}, true
		case T_UINT64:
			return overloadSpec{op: NE_UINT, arity: 2, result: T_BOOL}, true
		case T_FLOAT64:
			return overloadSpec{op: NE_FLOAT, arity: 2, result: T_BOOL}, true
		case T_BOOL:
			return overloadSpec{op: NE_INT, arity: 2, result: T_BOOL}, true
		case T_STRING:
			return overloadSpec{op: CALL_BUILTIN, builtin: BuiltinStrNe, arity: 2, result: T_BOOL}, true
		}
	}
	return overloadSpec{}, false
}

func (t *translator) resolveStructFieldIndex() (map[string][]int, error) {
	fields := make(map[string][]int)
	collisions := make(map[string]bool)
	addField := func(name string, idx []int) {
		if name == "" {
			return
		}
		if existing, found := fields[name]; found && !slices.Equal(existing, idx) {
			collisions[name] = true
			return
		}
		fields[name] = idx
	}
	for _, field := range reflect.VisibleFields(t.structType) {
		if !field.IsExported() {
			continue
		}
		addField(field.Name, field.Index)
		if tag := field.Tag.Get("json"); tag != "" {
			name := strings.Split(tag, ",")[0]
			if name != "" && name != "-" {
				addField(name, field.Index)
			}
		}
		lowerName := toLowerFirst(field.Name)
		if lowerName != field.Name {
			addField(lowerName, field.Index)
		}
	}
	if len(collisions) != 0 {
		names := make([]string, 0, len(collisions))
		for name := range collisions {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("jit struct: ambiguous field aliases in %v: %s", t.structType, strings.Join(names, ", "))
	}
	return fields, nil
}
