//go:build amd64

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
	"runtime"

	"github.com/twitchyliquid64/golang-asm/asm/arch"
	"github.com/twitchyliquid64/golang-asm/obj"
	asmx86 "github.com/twitchyliquid64/golang-asm/obj/x86"
	"github.com/twitchyliquid64/golang-asm/objabi"
)

// Frame layout after prologue:
//
//   high address
//   +------------------------------------------+
//   | saved struct ptr [SP+amd64StructBaseOff] |
//   +------------------------------------------+
//   | helper call area / spill region          |
//   | [SP ... SP+amd64StructBaseOff)           |
//   +------------------------------------------+  <- SP
//   low address

const (
	regArg    = 0
	regRet    = 0
	regTmpInt = 13 // Dedicated scratch for codegen (not allocated).
)

const (
	// Saved struct pointer, used to reload struct-field base across helper calls.
	amd64StructBaseOff = 64
	// Keep stack 8-byte aligned.
	amd64FrameBytes = 72

	nativeFrameBytes = amd64FrameBytes
)

func newNativeAsmContext() (*obj.Link, *arch.Arch) {
	asmArch := arch.Set("amd64")
	if asmArch == nil {
		return nil, nil
	}
	asmCtxt := obj.Linknew(asmArch.LinkArch)
	asmCtxt.DiagFunc = func(string, ...interface{}) {}
	if err := asmCtxt.Headtype.Set(runtime.GOOS); err != nil {
		asmCtxt.Headtype = objabi.Hlinux
	}
	asmArch.Init(asmCtxt)
	return asmCtxt, asmArch
}

// emitPrologue emits frame setup and struct pointer save:
// SUBQ SP, #frame; MOVQ struct, [SP+amd64StructBaseOff].
func (as *assembler) emitPrologue() {
	as.emitSubImmSP(amd64FrameBytes)
	as.emitStoreMemInt(regArg, asmx86.REG_SP, amd64StructBaseOff)
}

// emitEpilogue emits frame teardown and return:
// ADDQ SP, #frame; RET.
func (as *assembler) emitEpilogue() {
	as.emitAddImmSP(amd64FrameBytes)
	as.emitRet()
}

// emitInstr lowers one IR instruction to one or more AMD64 instructions.
func (as *assembler) emitInstr(ins Instr) error {
	switch ins.Op {
	case CONST_INT, CONST_UINT:
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		as.emitLoadImm(rd, uint64(ins.Imm))
		return nil
	case CONST_FLOAT:
		fd, err := as.floatRegOf(ins.Dst)
		if err != nil {
			return err
		}
		as.emitLoadImm(regTmpInt, uint64(ins.Imm))
		as.emitMoveIntToFloat(fd, regTmpInt)
		return nil
	case CONST_BOOL:
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		if ins.Imm == 0 {
			as.emitLoadImm(rd, 0)
		} else {
			as.emitLoadImm(rd, 1)
		}
		return nil
	case CONST_STRING:
		if ins.Imm < 0 || int(ins.Imm) >= len(as.stringPool) {
			return fmt.Errorf("%w: CONST_STRING index %d out of range (pool size=%d)", ErrCodegenUnsupported, ins.Imm, len(as.stringPool))
		}
		dstPtr, dstLen, err := as.stringRegsOf(ins.Dst)
		if err != nil {
			return err
		}
		ptr, ln := extractString(as.stringPool[int(ins.Imm)])
		as.emitLoadImm(dstPtr, ptr)
		as.emitLoadImm(dstLen, ln)
		return nil
	case LOAD_FIELD:
		if ins.Imm < 0 {
			return fmt.Errorf("%w: LOAD_FIELD offset must be non-negative, got %d", ErrCodegenUnsupported, ins.Imm)
		}
		offset := int32(ins.Imm)
		switch ins.Type {
		case T_INT64, T_UINT64:
			rd, err := as.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			as.emitLoadMemInt(rd, asmIntReg(regTmpInt), 0)
			return nil
		case T_BOOL:
			rd, err := as.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			as.emitLoadMemBool(rd, asmIntReg(regTmpInt), 0)
			return nil
		case T_FLOAT64:
			fd, err := as.floatRegOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			as.emitLoadMemFloat(fd, asmIntReg(regTmpInt), 0)
			return nil
		case T_STRING:
			dstPtr, dstLen, err := as.stringRegsOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			as.emitLoadMemInt(dstPtr, asmIntReg(regTmpInt), 0)
			as.emitLoadMemInt(dstLen, asmIntReg(regTmpInt), 8)
			return nil
		default:
			return fmt.Errorf("%w: LOAD_FIELD type %v is unsupported on amd64", ErrCodegenUnsupported, ins.Type)
		}
	case LOAD_FIELD_SLICE, LOAD_FIELD_ARRAY:
		if ins.Imm < 0 {
			return fmt.Errorf("%w: %v offset must be non-negative, got %d", ErrCodegenUnsupported, ins.Op, ins.Imm)
		}
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		as.emitLoadStructFieldAddr(rd, int32(ins.Imm))
		return nil
	case ADD_INT, ADD_UINT:
		return as.emitBinInt(ins, asmx86.AADDQ)
	case SUB_INT, SUB_UINT:
		return as.emitBinInt(ins, asmx86.ASUBQ)
	case MUL_INT, MUL_UINT:
		return as.emitMulInt(ins)
	case DIV_INT:
		return as.emitDivInt(ins, true)
	case MOD_INT:
		return as.emitModInt(ins, true)
	case NEG_INT:
		return as.emitNegInt(ins)
	case DIV_UINT:
		return as.emitDivInt(ins, false)
	case MOD_UINT:
		return as.emitModInt(ins, false)
	case ADD_FLOAT:
		return as.emitBinFloat(ins, asmx86.AADDSD)
	case SUB_FLOAT:
		return as.emitBinFloat(ins, asmx86.ASUBSD)
	case MUL_FLOAT:
		return as.emitBinFloat(ins, asmx86.AMULSD)
	case DIV_FLOAT:
		return as.emitBinFloat(ins, asmx86.ADIVSD)
	case NEG_FLOAT:
		return as.emitNegFloat(ins)
	case EQ_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmx86.ASETEQ)
	case NE_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmx86.ASETNE)
	case GT_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmx86.ASETHI)
	case GE_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmx86.ASETCC)
	case LT_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmx86.ASETCS)
	case LE_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmx86.ASETLS)
	case EQ_INT, EQ_UINT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETEQ)
	case NE_INT, NE_UINT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETNE)
	case GT_INT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETGT)
	case GE_INT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETGE)
	case LT_INT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETLT)
	case LE_INT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETLE)
	case LT_UINT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETCS)
	case LE_UINT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETLS)
	case GT_UINT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETHI)
	case GE_UINT:
		return as.emitCompareSetBoolInt(ins, asmx86.ASETCC)
	case MOVE:
		return as.emitMove(ins)
	case NOT:
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		rs, err := as.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitTestReg(rs)
		as.emitSetBoolByte(rd, asmx86.ASETEQ)
		as.emitMoveBoolToInt(rd)
		return nil
	case BR:
		return as.emitBranch(ins, obj.AJMP)
	case BR_TRUE:
		rs, err := as.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitTestReg(rs)
		return as.emitBranch(ins, asmx86.AJNE)
	case BR_FALSE:
		rs, err := as.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitTestReg(rs)
		return as.emitBranch(ins, asmx86.AJEQ)
	case LABEL:
		return nil
	case RETURN:
		rs, err := as.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		if rs != regRet {
			as.emitMoveIntToInt(regRet, rs)
		}
		as.emitEpilogue()
		return nil
	case CALL_BUILTIN:
		return as.emitCallBuiltin(ins)
	default:
		return fmt.Errorf("%w: opcode %v is unsupported on amd64", ErrCodegenUnsupported, ins.Op)
	}
}

// emitCallBuiltin dispatches CALL_BUILTIN lowering by BuiltinID.
func (as *assembler) emitCallBuiltin(ins Instr) error {
	fn, ok := builtinFunction(ins.BuiltinID)
	if !ok {
		return fmt.Errorf("%w: builtin %v has no function value", ErrCodegenUnsupported, ins.BuiltinID)
	}
	switch ins.BuiltinID {
	case BuiltinStrSize:
		return as.emitCallStringToInt(ins, fn)
	case BuiltinStrEq, BuiltinStrNe, BuiltinStrContains, BuiltinStrStarts, BuiltinStrEnds:
		return as.emitCallTwoStringsToBool(ins, fn)
	case BuiltinStrConcat:
		return as.emitCallTwoStringsToString(ins, fn)
	case BuiltinListContainsStringSlice:
		return as.emitCallSliceToBool(ins, fn)
	case BuiltinListContainsStringArray:
		return as.emitCallArrayToBool(ins, fn)
	default:
		return fmt.Errorf("%w: builtin %v is unsupported on amd64", ErrCodegenUnsupported, ins.BuiltinID)
	}
}

// emitCallStringToInt emits string -> int64 helper call:
// move args to ABI int arg regs, CALL fn, result in AX.
func (as *assembler) emitCallStringToInt(ins Instr, fn uintptr) error {
	dst, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	aPtr, aLen, err := as.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	as.emitMoveStringToArgs(0, aPtr, aLen)
	if err := as.emitCallFunction(fn); err != nil {
		return err
	}
	if dst != 0 {
		as.emitMoveFromAsmReg(dst, asmx86.REG_AX)
	}
	return nil
}

// emitCallTwoStringsToBool emits string,string -> bool helper call:
// move args to ABI int arg regs, CALL fn, bool result in AL/AX.
func (as *assembler) emitCallTwoStringsToBool(ins Instr, fn uintptr) error {
	dst, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	aPtr, aLen, err := as.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	bPtr, bLen, err := as.stringRegsOf(ins.Src2)
	if err != nil {
		return err
	}
	as.emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen)
	if err := as.emitCallFunction(fn); err != nil {
		return err
	}
	as.emitMoveBoolFromAX(dst)
	return nil
}

// emitCallTwoStringsToString emits string,string -> string helper call:
// move args to ABI int arg regs, CALL fn, result in AX/BX.
func (as *assembler) emitCallTwoStringsToString(ins Instr, fn uintptr) error {
	dstPtr, dstLen, err := as.stringRegsOf(ins.Dst)
	if err != nil {
		return err
	}
	aPtr, aLen, err := as.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	bPtr, bLen, err := as.stringRegsOf(ins.Src2)
	if err != nil {
		return err
	}
	as.emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen)
	if err := as.emitCallFunction(fn); err != nil {
		return err
	}
	if dstPtr != 0 {
		as.emitMoveFromAsmReg(dstPtr, asmx86.REG_AX)
	}
	if asmIntReg(dstLen) != asmx86.REG_BX {
		as.emitMoveFromAsmReg(dstLen, asmx86.REG_BX)
	}
	return nil
}

// emitCallSliceToBool emits (string, *sliceHeader) -> bool helper call:
// AX/BX=needle ptr/len, CX=list field address, CALL fn, bool result in AL/AX.
func (as *assembler) emitCallSliceToBool(ins Instr, fn uintptr) error {
	dst, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	needlePtr, needleLen, err := as.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	listAddr, err := as.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	as.emitParallelIntMoves([][2]uint32{
		{uint32(asmArgReg(0)), uint32(asmIntReg(needlePtr))},
		{uint32(asmArgReg(1)), uint32(asmIntReg(needleLen))},
		{uint32(asmArgReg(2)), uint32(asmIntReg(listAddr))},
	})
	if err := as.emitCallFunction(fn); err != nil {
		return err
	}
	as.emitMoveBoolFromAX(dst)
	return nil
}

// emitCallArrayToBool emits (string, *array, len) -> bool helper call:
// AX/BX=needle ptr/len, CX=array address, DI=len, CALL fn, bool result in AL/AX.
func (as *assembler) emitCallArrayToBool(ins Instr, fn uintptr) error {
	if ins.Imm < 0 {
		return fmt.Errorf("%w: array length must be non-negative, got %d", ErrCodegenUnsupported, ins.Imm)
	}
	dst, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	needlePtr, needleLen, err := as.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	arrayAddr, err := as.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	as.emitLoadImm(regTmpInt, uint64(ins.Imm))
	as.emitParallelIntMoves([][2]uint32{
		{uint32(asmArgReg(0)), uint32(asmIntReg(needlePtr))},
		{uint32(asmArgReg(1)), uint32(asmIntReg(needleLen))},
		{uint32(asmArgReg(2)), uint32(asmIntReg(arrayAddr))},
		{uint32(asmArgReg(3)), uint32(asmIntReg(regTmpInt))},
	})
	if err := as.emitCallFunction(fn); err != nil {
		return err
	}
	as.emitMoveBoolFromAX(dst)
	return nil
}

// emitMoveStringToArgs moves one string pair to ABI argument registers:
// arg[argBase]=ptr, arg[argBase+1]=len.
func (as *assembler) emitMoveStringToArgs(argBase, ptrReg, lenReg uint32) {
	as.emitParallelIntMoves([][2]uint32{
		{uint32(asmArgReg(int(argBase))), uint32(asmIntReg(ptrReg))},
		{uint32(asmArgReg(int(argBase + 1))), uint32(asmIntReg(lenReg))},
	})
}

// emitMoveTwoStringsToArgs moves two string pairs to ABI argument registers:
// arg0/1=lhs ptr/len, arg2/3=rhs ptr/len.
func (as *assembler) emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen uint32) {
	as.emitParallelIntMoves([][2]uint32{
		{uint32(asmArgReg(0)), uint32(asmIntReg(aPtr))},
		{uint32(asmArgReg(1)), uint32(asmIntReg(aLen))},
		{uint32(asmArgReg(2)), uint32(asmIntReg(bPtr))},
		{uint32(asmArgReg(3)), uint32(asmIntReg(bLen))},
	})
}

// emitParallelIntMoves performs parallel register moves through stack spills:
// MOVQ src_i, [SP+off_i] then MOVQ [SP+off_i], dst_i.
func (as *assembler) emitParallelIntMoves(moves [][2]uint32) {
	if len(moves) == 0 {
		return
	}
	count := 0
	for _, m := range moves {
		if m[0] == m[1] {
			continue
		}
		off := int32(count * 8)
		as.emitStoreMemFromAsmReg(int16(m[1]), asmx86.REG_SP, off)
		count++
	}
	if count == 0 {
		return
	}
	count = 0
	for _, m := range moves {
		if m[0] == m[1] {
			continue
		}
		off := int32(count * 8)
		as.emitLoadMemToAsmReg(int16(m[0]), asmx86.REG_SP, off)
		count++
	}
}

// emitCallFunction emits a function-value indirect call sequence:
// MOVQ $fn, R13; MOVQ R13, DX; MOVQ (R13), R13; CALL R13.
func (as *assembler) emitCallFunction(fn uintptr) error {
	if fn == 0 {
		return fmt.Errorf("%w: helper function pointer is nil", ErrCodegenUnsupported)
	}
	as.emitLoadImm(regTmpInt, uint64(fn))
	// RDX is the closure context register under ABIInternal.
	as.emitMoveIntToInt(2, regTmpInt)
	as.emitLoadMemInt(regTmpInt, asmIntReg(regTmpInt), 0)
	as.emitCallReg(regTmpInt)
	return nil
}

// emitMove emits typed MOVE lowering:
// MOVSD for float, two MOVQ for string pair, MOVQ for integer/bool.
func (as *assembler) emitMove(ins Instr) error {
	switch ins.Type {
	case T_FLOAT64:
		fd, err := as.floatRegOf(ins.Dst)
		if err != nil {
			return err
		}
		fs, err := as.floatRegOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitMoveFloatToFloat(fd, fs)
		return nil
	case T_STRING:
		d0, d1, err := as.stringRegsOf(ins.Dst)
		if err != nil {
			return err
		}
		s0, s1, err := as.stringRegsOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitMoveIntToInt(d0, s0)
		as.emitMoveIntToInt(d1, s1)
		return nil
	default:
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		rs, err := as.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitMoveIntToInt(rd, rs)
		return nil
	}
}

// emitBinInt emits integer binary op in two-address form:
// MOVQ Rdst, Rsrc1; OP Rdst, Rsrc2.
func (as *assembler) emitBinInt(ins Instr, op obj.As) error {
	rd, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := as.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := as.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	if rd != rn {
		as.emitMoveIntToInt(rd, rn)
	}
	p := as.asmCtxt.NewProg()
	p.As = op
	p.From = regAddr(asmIntReg(rm))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
	return nil
}

// emitMulInt emits integer multiply:
// IMULQ Rdst, Rsrc2 (after moving src1 into dst).
func (as *assembler) emitMulInt(ins Instr) error {
	return as.emitBinInt(ins, asmx86.AIMULQ)
}

// emitDivInt emits signed/unsigned integer divide:
// AX=src1; CQO/XOR DX,DX; IDIVQ/DIVQ src2; quotient in AX.
func (as *assembler) emitDivInt(ins Instr, signed bool) error {
	rd, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := as.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := as.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	divisor := rm
	if divisor == 0 || divisor == 2 {
		as.emitMoveIntToInt(regTmpInt, divisor)
		divisor = regTmpInt
	}
	if rn != 0 {
		as.emitMoveIntToInt(0, rn)
	}
	if signed {
		p := as.asmCtxt.NewProg()
		p.As = asmx86.ACQO
		as.registerProg(p)
	} else {
		p := as.asmCtxt.NewProg()
		p.As = asmx86.AXORQ
		p.From = regAddr(asmx86.REG_DX)
		p.To = regAddr(asmx86.REG_DX)
		as.registerProg(p)
	}
	p := as.asmCtxt.NewProg()
	if signed {
		p.As = asmx86.AIDIVQ
	} else {
		p.As = asmx86.ADIVQ
	}
	p.From = regAddr(asmIntReg(divisor))
	as.registerProg(p)
	if rd != 0 {
		as.emitMoveIntToInt(rd, 0)
	}
	return nil
}

// emitModInt emits signed/unsigned integer remainder:
// AX=src1; CQO/XOR DX,DX; IDIVQ/DIVQ src2; remainder in DX.
func (as *assembler) emitModInt(ins Instr, signed bool) error {
	rd, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := as.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := as.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	divisor := rm
	if divisor == 0 || divisor == 2 {
		as.emitMoveIntToInt(regTmpInt, divisor)
		divisor = regTmpInt
	}
	if rn != 0 {
		as.emitMoveIntToInt(0, rn)
	}
	if signed {
		p := as.asmCtxt.NewProg()
		p.As = asmx86.ACQO
		as.registerProg(p)
	} else {
		p := as.asmCtxt.NewProg()
		p.As = asmx86.AXORQ
		p.From = regAddr(asmx86.REG_DX)
		p.To = regAddr(asmx86.REG_DX)
		as.registerProg(p)
	}
	p := as.asmCtxt.NewProg()
	if signed {
		p.As = asmx86.AIDIVQ
	} else {
		p.As = asmx86.ADIVQ
	}
	p.From = regAddr(asmIntReg(divisor))
	as.registerProg(p)
	if rd != 2 {
		as.emitMoveIntToInt(rd, 2)
	}
	return nil
}

// emitNegInt emits integer negate:
// MOVQ Rdst, Rsrc; NEGQ Rdst.
func (as *assembler) emitNegInt(ins Instr) error {
	rd, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rs, err := as.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	if rd != rs {
		as.emitMoveIntToInt(rd, rs)
	}
	p := as.asmCtxt.NewProg()
	p.As = asmx86.ANEGQ
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
	return nil
}

// emitBinFloat emits float binary op in two-address form:
// MOVSD Xdst, Xsrc1; FOP Xdst, Xsrc2.
func (as *assembler) emitBinFloat(ins Instr, op obj.As) error {
	fd, err := as.floatRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fn, err := as.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	fm, err := as.floatRegOf(ins.Src2)
	if err != nil {
		return err
	}
	if fd != fn {
		as.emitMoveFloatToFloat(fd, fn)
	}
	p := as.asmCtxt.NewProg()
	p.As = op
	p.From = regAddr(asmFloatReg(fm))
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
	return nil
}

// emitNegFloat emits float negate by toggling sign bit:
// MOVQ Rtmp, Xsrc; BTCQ Rtmp, 63; MOVQ Xdst, Rtmp.
func (as *assembler) emitNegFloat(ins Instr) error {
	fd, err := as.floatRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fs, err := as.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	as.emitMoveFloatToInt(regTmpInt, fs)
	p := as.asmCtxt.NewProg()
	p.As = asmx86.ABTCQ
	p.From = constAddr(63)
	p.To = regAddr(asmIntReg(regTmpInt))
	as.registerProg(p)
	as.emitMoveIntToFloat(fd, regTmpInt)
	return nil
}

// emitCompareSetBoolInt emits integer compare+set:
// CMPQ Rsrc1, Rsrc2; SETcc Rdst8; MOVBQZX Rdst, Rdst8.
func (as *assembler) emitCompareSetBoolInt(ins Instr, op obj.As) error {
	rd, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := as.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := as.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	as.emitCmpRegInt(rn, rm)
	as.emitSetBoolByte(rd, op)
	as.emitMoveBoolToInt(rd)
	return nil
}

// emitCompareSetBoolFloat emits float compare+set:
// UCOMISD Xsrc1, Xsrc2; SETcc handling includes unordered (NaN) semantics.
func (as *assembler) emitCompareSetBoolFloat(ins Instr, op obj.As) error {
	rd, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fn, err := as.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	fm, err := as.floatRegOf(ins.Src2)
	if err != nil {
		return err
	}
	as.emitCmpRegFloat(fn, fm)
	switch op {
	case asmx86.ASETEQ:
		as.emitSetBoolByte(rd, asmx86.ASETEQ)
		as.emitSetBoolByte(regTmpInt, asmx86.ASETPC)
		as.emitAndByte(rd, regTmpInt)
		as.emitMoveBoolToInt(rd)
		return nil
	case asmx86.ASETNE:
		as.emitSetBoolByte(rd, asmx86.ASETNE)
		as.emitSetBoolByte(regTmpInt, asmx86.ASETPS)
		as.emitOrByte(rd, regTmpInt)
		as.emitMoveBoolToInt(rd)
		return nil
	case asmx86.ASETCS, asmx86.ASETLS:
		as.emitSetBoolByte(rd, op)
		as.emitSetBoolByte(regTmpInt, asmx86.ASETPC)
		as.emitAndByte(rd, regTmpInt)
		as.emitMoveBoolToInt(rd)
		return nil
	default:
		as.emitSetBoolByte(rd, op)
		as.emitMoveBoolToInt(rd)
		return nil
	}
}

// emitSetBoolByte emits SETcc into an 8-bit destination register.
func (as *assembler) emitSetBoolByte(rd uint32, op obj.As) {
	p := as.asmCtxt.NewProg()
	p.As = op
	p.To = regAddr(asmInt8Reg(asmIntReg(rd)))
	as.registerProg(p)
}

// emitAndByte emits 8-bit AND: dst8 &= src8.
func (as *assembler) emitAndByte(dst, src uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AANDB
	p.From = regAddr(asmInt8Reg(asmIntReg(src)))
	p.To = regAddr(asmInt8Reg(asmIntReg(dst)))
	as.registerProg(p)
}

// emitOrByte emits 8-bit OR: dst8 |= src8.
func (as *assembler) emitOrByte(dst, src uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AORB
	p.From = regAddr(asmInt8Reg(asmIntReg(src)))
	p.To = regAddr(asmInt8Reg(asmIntReg(dst)))
	as.registerProg(p)
}

// emitCmpRegInt emits integer compare:
// CMPQ Rrn, Rrm.
func (as *assembler) emitCmpRegInt(rn, rm uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.ACMPQ
	p.From = regAddr(asmIntReg(rn))
	p.To = regAddr(asmIntReg(rm))
	as.registerProg(p)
}

// emitCmpRegFloat emits floating compare:
// UCOMISD Xfn, Xfm.
func (as *assembler) emitCmpRegFloat(fn, fm uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AUCOMISD
	p.From = regAddr(asmFloatReg(fm))
	p.To = regAddr(asmFloatReg(fn))
	as.registerProg(p)
}

// emitBranch emits JMP/Jcc with unresolved TYPE_BRANCH target.
func (as *assembler) emitBranch(ins Instr, op obj.As) error {
	p := as.asmCtxt.NewProg()
	p.As = op
	p.To.Type = obj.TYPE_BRANCH
	as.registerProg(p)
	as.registerBranch(int(ins.Lbl), p)
	return nil
}

// emitTestReg emits integer test:
// TESTQ Rrn, Rrn.
func (as *assembler) emitTestReg(rn uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.ATESTQ
	p.From = regAddr(asmIntReg(rn))
	p.To = regAddr(asmIntReg(rn))
	as.registerProg(p)
}

// emitMoveFromAsmReg emits integer move from raw asm register to virtual dst register.
func (as *assembler) emitMoveFromAsmReg(dst uint32, src int16) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(src)
	p.To = regAddr(asmIntReg(dst))
	as.registerProg(p)
}

// emitLoadMemToAsmReg emits 64-bit load into raw asm register:
// MOVQ dst, [base+off].
func (as *assembler) emitLoadMemToAsmReg(dst int16, base int16, off int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = memAddr(base, off)
	p.To = regAddr(dst)
	as.registerProg(p)
}

// emitStoreMemFromAsmReg emits 64-bit store from raw asm register:
// MOVQ [base+off], src.
func (as *assembler) emitStoreMemFromAsmReg(src int16, base int16, off int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(src)
	p.To = memAddr(base, off)
	as.registerProg(p)
}

// emitMoveBoolFromAX moves AL bool result to a full-width destination register.
func (as *assembler) emitMoveBoolFromAX(dst uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVBQZX
	p.From = regAddr(asmx86.REG_AL)
	p.To = regAddr(asmIntReg(dst))
	as.registerProg(p)
}

// emitMoveIntToInt emits integer register move:
// MOVQ Rrd, Rrs.
func (as *assembler) emitMoveIntToInt(rd, rs uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitMoveFloatToFloat emits float register move:
// MOVSD Xfd, Xfs.
func (as *assembler) emitMoveFloatToFloat(fd, fs uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVSD
	p.From = regAddr(asmFloatReg(fs))
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
}

// emitMoveIntToFloat emits integer->float register bit-move:
// MOVQ Xfd, Rrs.
func (as *assembler) emitMoveIntToFloat(fd, rs uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
}

// emitMoveFloatToInt emits float->integer register bit-move:
// MOVQ Rrd, Xfs.
func (as *assembler) emitMoveFloatToInt(rd, fs uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmFloatReg(fs))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitMoveBoolToInt zero-extends 8-bit bool into 64-bit register.
func (as *assembler) emitMoveBoolToInt(rd uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVBQZX
	p.From = regAddr(asmInt8Reg(asmIntReg(rd)))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitLoadImm emits constant materialization:
// MOVQ Rrd, $imm.
func (as *assembler) emitLoadImm(rd uint32, imm uint64) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = constAddr(int64(imm))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitLoadMemInt emits 64-bit load:
// MOVQ Rrd, [base+off].
func (as *assembler) emitLoadMemInt(rd uint32, base int16, off int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = memAddr(base, off)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitStoreMemInt emits 64-bit store:
// MOVQ [base+off], Rrs.
func (as *assembler) emitStoreMemInt(rs uint32, base int16, off int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmIntReg(rs))
	p.To = memAddr(base, off)
	as.registerProg(p)
}

// emitLoadMemBool emits bool load with zero-extension:
// MOVBQZX Rrd, byte [base+off].
func (as *assembler) emitLoadMemBool(rd uint32, base int16, off int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVBQZX
	p.From = memAddr(base, off)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitLoadMemFloat emits float64 load:
// MOVSD Xfd, [base+off].
func (as *assembler) emitLoadMemFloat(fd uint32, base int16, off int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AMOVSD
	p.From = memAddr(base, off)
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
}

// emitAddImmSP emits stack pointer increment:
// ADDQ SP, #imm.
func (as *assembler) emitAddImmSP(imm int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.AADDQ
	p.From = constAddr(int64(imm))
	p.To = regAddr(asmx86.REG_SP)
	as.registerProg(p)
}

// emitSubImmSP emits stack pointer decrement:
// SUBQ SP, #imm.
func (as *assembler) emitSubImmSP(imm int32) {
	p := as.asmCtxt.NewProg()
	p.As = asmx86.ASUBQ
	p.From = constAddr(int64(imm))
	p.To = regAddr(asmx86.REG_SP)
	as.registerProg(p)
}

// emitCallReg emits indirect call:
// CALL Rreg.
func (as *assembler) emitCallReg(reg uint32) {
	p := as.asmCtxt.NewProg()
	p.As = obj.ACALL
	p.To = regAddr(asmIntReg(reg))
	as.registerProg(p)
}

// emitRet emits return instruction:
// RET.
func (as *assembler) emitRet() {
	p := as.asmCtxt.NewProg()
	p.As = obj.ARET
	as.registerProg(p)
}

// emitLoadStructBase reloads struct pointer shadow from stack frame.
func (as *assembler) emitLoadStructBase(rd uint32) {
	as.emitLoadMemToAsmReg(asmIntReg(rd), asmx86.REG_SP, amd64StructBaseOff)
}

// emitLoadStructFieldAddr emits field address materialization:
// Rrd = struct_base + offset.
func (as *assembler) emitLoadStructFieldAddr(rd uint32, offset int32) {
	as.emitLoadStructBase(rd)
	if offset == 0 {
		return
	}
	p := as.asmCtxt.NewProg()
	p.As = asmx86.ALEAQ
	p.From = memAddr(asmIntReg(rd), offset)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

func asmArgReg(idx int) int16 {
	switch idx {
	case 0:
		return asmx86.REG_AX
	case 1:
		return asmx86.REG_BX
	case 2:
		return asmx86.REG_CX
	case 3:
		return asmx86.REG_DI
	case 4:
		return asmx86.REG_SI
	case 5:
		return asmx86.REG_R8
	case 6:
		return asmx86.REG_R9
	case 7:
		return asmx86.REG_R10
	case 8:
		return asmx86.REG_R11
	default:
		return asmx86.REG_AX
	}
}

func asmIntReg(reg uint32) int16 {
	switch reg {
	case 0:
		return asmx86.REG_AX + 0
	case 1:
		return asmx86.REG_AX + 1
	case 2:
		return asmx86.REG_AX + 2
	case 3:
		return asmx86.REG_AX + 3
	case 4:
		return asmx86.REG_AX + 4
	case 5:
		return asmx86.REG_AX + 5
	case 6:
		return asmx86.REG_AX + 6
	case 7:
		return asmx86.REG_AX + 7
	case 8:
		return asmx86.REG_AX + 8
	case 9:
		return asmx86.REG_AX + 9
	case 10:
		return asmx86.REG_AX + 10
	case 11:
		return asmx86.REG_AX + 11
	case 12:
		return asmx86.REG_AX + 12
	case 13:
		return asmx86.REG_AX + 13
	default:
		return asmx86.REG_AX
	}
}

func asmInt8Reg(reg int16) int16 {
	switch reg {
	case asmx86.REG_AX:
		return asmx86.REG_AL
	case asmx86.REG_BX:
		return asmx86.REG_BL
	case asmx86.REG_CX:
		return asmx86.REG_CL
	case asmx86.REG_DX:
		return asmx86.REG_DL
	case asmx86.REG_SI:
		return asmx86.REG_SIB
	case asmx86.REG_DI:
		return asmx86.REG_DIB
	case asmx86.REG_R8:
		return asmx86.REG_R8B
	case asmx86.REG_R9:
		return asmx86.REG_R9B
	case asmx86.REG_R10:
		return asmx86.REG_R10B
	case asmx86.REG_R11:
		return asmx86.REG_R11B
	case asmx86.REG_R12:
		return asmx86.REG_R12B
	case asmx86.REG_R13:
		return asmx86.REG_R13B
	default:
		return asmx86.REG_AL
	}
}

func asmFloatReg(reg uint32) int16 {
	switch reg {
	case 0:
		return asmx86.REG_X0
	case 1:
		return asmx86.REG_X1
	case 2:
		return asmx86.REG_X2
	case 3:
		return asmx86.REG_X3
	case 4:
		return asmx86.REG_X4
	case 5:
		return asmx86.REG_X5
	case 6:
		return asmx86.REG_X6
	case 7:
		return asmx86.REG_X7
	case 8:
		return asmx86.REG_X8
	case 9:
		return asmx86.REG_X9
	case 10:
		return asmx86.REG_X10
	case 11:
		return asmx86.REG_X11
	case 12:
		return asmx86.REG_X12
	case 13:
		return asmx86.REG_X13
	case 14:
		return asmx86.REG_X14
	case 15:
		return asmx86.REG_X15
	default:
		return asmx86.REG_X0
	}
}
