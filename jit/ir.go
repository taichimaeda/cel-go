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

// Opcode is the operation identifier for a TAC instruction.
type Opcode uint16

// VReg identifies a virtual register.
type VReg uint32

// Type is the static type associated with a virtual register value.
type Type uint8

// Label references an instruction index after label resolution.
type Label int

// BuiltinID identifies a pre-registered builtin helper.
type BuiltinID uint16

const (
	T_UNSPECIFIED Type = iota
	T_INT64
	T_UINT64
	T_FLOAT64
	T_BOOL
	T_STRING
)

const (
	BuiltinStrEq BuiltinID = iota
	BuiltinStrNe
	BuiltinStrContains
	BuiltinStrStarts
	BuiltinStrEnds
	BuiltinStrConcat
	BuiltinStrSize
	BuiltinListContainsStringSlice
	BuiltinListContainsStringArray
)

const (
	OP_UNSPECIFIED Opcode = iota

	// Constants.
	CONST_INT
	CONST_UINT
	CONST_FLOAT
	CONST_BOOL
	CONST_STRING

	// Slot / type conversion.
	LOAD_FIELD
	LOAD_FIELD_SLICE
	LOAD_FIELD_ARRAY

	// Arithmetic.
	ADD_INT
	SUB_INT
	MUL_INT
	DIV_INT
	MOD_INT
	NEG_INT
	ADD_UINT
	SUB_UINT
	MUL_UINT
	DIV_UINT
	MOD_UINT
	ADD_FLOAT
	SUB_FLOAT
	MUL_FLOAT
	DIV_FLOAT
	NEG_FLOAT

	// Comparisons.
	EQ_INT
	NE_INT
	LT_INT
	LE_INT
	GT_INT
	GE_INT
	EQ_UINT
	NE_UINT
	LT_UINT
	LE_UINT
	GT_UINT
	GE_UINT
	EQ_FLOAT
	NE_FLOAT
	LT_FLOAT
	LE_FLOAT
	GT_FLOAT
	GE_FLOAT

	// Logical and misc.
	NOT
	MOVE

	// Control flow.
	LABEL
	BR
	BR_TRUE
	BR_FALSE

	// Calls and returns.
	CALL_BUILTIN
	RETURN
)

// Instr is a TAC instruction.
type Instr struct {
	Op        Opcode
	Dst       VReg
	Src1      VReg
	Src2      VReg
	Imm       int64
	Lbl       Label
	Type      Type
	BuiltinID BuiltinID
}

// Program is the output of the translate pass.
type Program struct {
	Instrs     []Instr
	NumVRegs   int
	StringPool []string
}

func (t Type) String() string {
	switch t {
	case T_UNSPECIFIED:
		return "unspecified"
	case T_INT64:
		return "int64"
	case T_UINT64:
		return "uint64"
	case T_FLOAT64:
		return "float64"
	case T_BOOL:
		return "bool"
	case T_STRING:
		return "string"
	default:
		return "type(?)"
	}
}

func (b BuiltinID) String() string {
	switch b {
	case BuiltinStrEq:
		return "str_eq"
	case BuiltinStrNe:
		return "str_ne"
	case BuiltinStrContains:
		return "str_contains"
	case BuiltinStrStarts:
		return "str_starts"
	case BuiltinStrEnds:
		return "str_ends"
	case BuiltinStrConcat:
		return "str_concat"
	case BuiltinStrSize:
		return "str_size"
	case BuiltinListContainsStringSlice:
		return "list_contains_string_slice"
	case BuiltinListContainsStringArray:
		return "list_contains_string_array"
	default:
		return "builtin(?)"
	}
}

func (op Opcode) String() string {
	switch op {
	case OP_UNSPECIFIED:
		return "op_unspecified"
	case CONST_INT:
		return "const_int"
	case CONST_UINT:
		return "const_uint"
	case CONST_FLOAT:
		return "const_float"
	case CONST_BOOL:
		return "const_bool"
	case CONST_STRING:
		return "const_string"
	case LOAD_FIELD:
		return "load_field"
	case LOAD_FIELD_SLICE:
		return "load_field_slice"
	case LOAD_FIELD_ARRAY:
		return "load_field_array"
	case ADD_INT:
		return "add_int"
	case SUB_INT:
		return "sub_int"
	case MUL_INT:
		return "mul_int"
	case DIV_INT:
		return "div_int"
	case MOD_INT:
		return "mod_int"
	case NEG_INT:
		return "neg_int"
	case ADD_UINT:
		return "add_uint"
	case SUB_UINT:
		return "sub_uint"
	case MUL_UINT:
		return "mul_uint"
	case DIV_UINT:
		return "div_uint"
	case MOD_UINT:
		return "mod_uint"
	case ADD_FLOAT:
		return "add_float"
	case SUB_FLOAT:
		return "sub_float"
	case MUL_FLOAT:
		return "mul_float"
	case DIV_FLOAT:
		return "div_float"
	case NEG_FLOAT:
		return "neg_float"
	case EQ_INT:
		return "eq_int"
	case NE_INT:
		return "ne_int"
	case LT_INT:
		return "lt_int"
	case LE_INT:
		return "le_int"
	case GT_INT:
		return "gt_int"
	case GE_INT:
		return "ge_int"
	case EQ_UINT:
		return "eq_uint"
	case NE_UINT:
		return "ne_uint"
	case LT_UINT:
		return "lt_uint"
	case LE_UINT:
		return "le_uint"
	case GT_UINT:
		return "gt_uint"
	case GE_UINT:
		return "ge_uint"
	case EQ_FLOAT:
		return "eq_float"
	case NE_FLOAT:
		return "ne_float"
	case LT_FLOAT:
		return "lt_float"
	case LE_FLOAT:
		return "le_float"
	case GT_FLOAT:
		return "gt_float"
	case GE_FLOAT:
		return "ge_float"
	case NOT:
		return "not"
	case MOVE:
		return "move"
	case LABEL:
		return "label"
	case BR:
		return "br"
	case BR_TRUE:
		return "br_true"
	case BR_FALSE:
		return "br_false"
	case CALL_BUILTIN:
		return "call_builtin"
	case RETURN:
		return "return"
	default:
		return "opcode(?)"
	}
}
