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

const (
	// Saved input pointer, used to reload struct-field base across helper calls.
	amd64InputBaseOff = 64
	// Keep stack 8-byte aligned.
	amd64FrameBytes  = 72
	nativeFrameBytes = amd64FrameBytes
)

const (
	regArgInput = 0
	regRet      = 0
	regIntTmp   = 13 // R13, dedicated scratch for codegen (not allocated).
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

// Frame layout after prologue:
//
//   high address
//   +-------------------------------+
//   | saved input ptr [SP+amd64InputBaseOff]
//   +-------------------------------+
//   | reserved spill/scratch area   |
//   | [SP ... SP+amd64InputBaseOff)
//   +-------------------------------+  <- SP
//   low address

// emitPrologue emits frame setup and input pointer save:
// SUBQ SP, #frame; MOVQ input, [SP+amd64InputBaseOff].
func (g *assembler) emitPrologue() {
	g.emitSubImmSP(amd64FrameBytes)
	g.emitStoreMem64(regArgInput, asmx86.REG_SP, amd64InputBaseOff)
}

// emitEpilogue emits frame teardown and return:
// ADDQ SP, #frame; RET.
func (g *assembler) emitEpilogue() {
	g.emitAddImmSP(amd64FrameBytes)
	p := g.asmCtxt.NewProg()
	p.As = obj.ARET
	g.registerProg(p)
}

// emitInstr lowers one IR instruction to one or more AMD64 instructions.
func (g *assembler) emitInstr(ins Instr) error {
	switch ins.Op {
	case CONST_INT, CONST_UINT:
		rd, err := g.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		g.emitLoadImm(rd, uint64(ins.Imm))
		return nil
	case CONST_FLOAT:
		fd, err := g.floatRegOf(ins.Dst)
		if err != nil {
			return err
		}
		g.emitLoadImm(regIntTmp, uint64(ins.Imm))
		g.emitMovIntToFloat(fd, regIntTmp)
		return nil
	case CONST_BOOL:
		rd, err := g.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		if ins.Imm == 0 {
			g.emitLoadImm(rd, 0)
		} else {
			g.emitLoadImm(rd, 1)
		}
		return nil
	case CONST_STRING:
		if ins.Imm < 0 || int(ins.Imm) >= len(g.stringPool) {
			return fmt.Errorf("%w: CONST_STRING index %d out of range (pool size=%d)", ErrCodegenUnsupported, ins.Imm, len(g.stringPool))
		}
		dstPtr, dstLen, err := g.stringRegsOf(ins.Dst)
		if err != nil {
			return err
		}
		ptr, ln := extractString(g.stringPool[int(ins.Imm)])
		g.emitLoadImm(dstPtr, ptr)
		g.emitLoadImm(dstLen, ln)
		return nil
	case LOAD_FIELD:
		if ins.Imm < 0 {
			return fmt.Errorf("%w: LOAD_FIELD offset must be non-negative, got %d", ErrCodegenUnsupported, ins.Imm)
		}
		offset := int32(ins.Imm)
		switch ins.Type {
		case T_INT64, T_UINT64:
			rd, err := g.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			g.emitLoadMem64(rd, asmIntReg(regIntTmp), 0)
			return nil
		case T_BOOL:
			rd, err := g.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			g.emitLoadMemBool(rd, asmIntReg(regIntTmp), 0)
			return nil
		case T_FLOAT64:
			fd, err := g.floatRegOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			g.emitLoadMemFloat(fd, asmIntReg(regIntTmp), 0)
			return nil
		case T_STRING:
			dstPtr, dstLen, err := g.stringRegsOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			g.emitLoadMem64(dstPtr, asmIntReg(regIntTmp), 0)
			g.emitLoadMem64(dstLen, asmIntReg(regIntTmp), 8)
			return nil
		default:
			return fmt.Errorf("%w: LOAD_FIELD type %v is unsupported on amd64", ErrCodegenUnsupported, ins.Type)
		}
	case LOAD_FIELD_SLICE, LOAD_FIELD_ARRAY:
		if ins.Imm < 0 {
			return fmt.Errorf("%w: %v offset must be non-negative, got %d", ErrCodegenUnsupported, ins.Op, ins.Imm)
		}
		rd, err := g.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		g.emitLoadFieldAddr(rd, int32(ins.Imm))
		return nil
	case ADD_INT, ADD_UINT:
		return g.emitBinary(ins, asmx86.AADDQ)
	case SUB_INT, SUB_UINT:
		return g.emitBinary(ins, asmx86.ASUBQ)
	case MUL_INT, MUL_UINT:
		return g.emitMulInt(ins)
	case DIV_INT:
		return g.emitDiv(ins, true)
	case MOD_INT:
		return g.emitMod(ins, true)
	case NEG_INT:
		return g.emitNegInt(ins)
	case DIV_UINT:
		return g.emitDiv(ins, false)
	case MOD_UINT:
		return g.emitMod(ins, false)
	case ADD_FLOAT:
		return g.emitBinaryFloat(ins, asmx86.AADDSD)
	case SUB_FLOAT:
		return g.emitBinaryFloat(ins, asmx86.ASUBSD)
	case MUL_FLOAT:
		return g.emitBinaryFloat(ins, asmx86.AMULSD)
	case DIV_FLOAT:
		return g.emitBinaryFloat(ins, asmx86.ADIVSD)
	case NEG_FLOAT:
		return g.emitNegFloat(ins)
	case EQ_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmx86.ASETEQ)
	case NE_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmx86.ASETNE)
	case GT_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmx86.ASETHI)
	case GE_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmx86.ASETCC)
	case LT_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmx86.ASETCS)
	case LE_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmx86.ASETLS)
	case EQ_INT, EQ_UINT:
		return g.emitCompareSetBool(ins, asmx86.ASETEQ)
	case NE_INT, NE_UINT:
		return g.emitCompareSetBool(ins, asmx86.ASETNE)
	case GT_INT:
		return g.emitCompareSetBool(ins, asmx86.ASETGT)
	case GE_INT:
		return g.emitCompareSetBool(ins, asmx86.ASETGE)
	case LT_INT:
		return g.emitCompareSetBool(ins, asmx86.ASETLT)
	case LE_INT:
		return g.emitCompareSetBool(ins, asmx86.ASETLE)
	case LT_UINT:
		return g.emitCompareSetBool(ins, asmx86.ASETCS)
	case LE_UINT:
		return g.emitCompareSetBool(ins, asmx86.ASETLS)
	case GT_UINT:
		return g.emitCompareSetBool(ins, asmx86.ASETHI)
	case GE_UINT:
		return g.emitCompareSetBool(ins, asmx86.ASETCC)
	case MOVE:
		return g.emitMove(ins)
	case NOT:
		rd, err := g.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		rs, err := g.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		g.emitTestReg(rs)
		return g.emitSetBool(rd, asmx86.ASETEQ)
	case BR:
		return g.emitBranch(ins, obj.AJMP)
	case BR_TRUE:
		rs, err := g.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		g.emitTestReg(rs)
		return g.emitBranch(ins, asmx86.AJNE)
	case BR_FALSE:
		rs, err := g.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		g.emitTestReg(rs)
		return g.emitBranch(ins, asmx86.AJEQ)
	case LABEL:
		return nil
	case RETURN:
		rs, err := g.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		if rs != regRet {
			g.emitMovReg(regRet, rs)
		}
		g.emitEpilogue()
		return nil
	case CALL_BUILTIN:
		return g.emitCallBuiltin(ins)
	default:
		return fmt.Errorf("%w: opcode %v is unsupported on amd64", ErrCodegenUnsupported, ins.Op)
	}
}

// emitCallBuiltin dispatches CALL_BUILTIN lowering by BuiltinID.
func (g *assembler) emitCallBuiltin(ins Instr) error {
	funcValue, ok := builtinFunctionValue(ins.BuiltinID)
	if !ok {
		return fmt.Errorf("%w: builtin %v has no function value", ErrCodegenUnsupported, ins.BuiltinID)
	}
	switch ins.BuiltinID {
	case BuiltinStrEq, BuiltinStrNe, BuiltinStrContains, BuiltinStrStarts, BuiltinStrEnds:
		return g.emitCallTwoStringToBool(ins, funcValue)
	case BuiltinStrConcat:
		return g.emitCallTwoStringToString(ins, funcValue)
	case BuiltinStrSize:
		return g.emitCallOneStringToInt(ins, funcValue)
	case BuiltinListContainsStringSlice:
		return g.emitCallSliceToBool(ins, funcValue)
	case BuiltinListContainsStringArray:
		return g.emitCallArrayToBool(ins, funcValue)
	default:
		return fmt.Errorf("%w: builtin %v is unsupported on amd64", ErrCodegenUnsupported, ins.BuiltinID)
	}
}

// emitCallTwoStringToBool emits string,string -> bool helper call:
// move args to ABI int arg regs, CALL fn, bool result in AL/AX.
func (g *assembler) emitCallTwoStringToBool(ins Instr, funcval uintptr) error {
	dst, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	aPtr, aLen, err := g.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	bPtr, bLen, err := g.stringRegsOf(ins.Src2)
	if err != nil {
		return err
	}
	g.emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen)
	if err := g.emitCallFunctionValue(funcval); err != nil {
		return err
	}
	g.emitMovBoolFromAX(dst)
	return nil
}

// emitCallTwoStringToString emits string,string -> string helper call:
// move args to ABI int arg regs, CALL fn, result in AX/BX.
func (g *assembler) emitCallTwoStringToString(ins Instr, funcval uintptr) error {
	dstPtr, dstLen, err := g.stringRegsOf(ins.Dst)
	if err != nil {
		return err
	}
	aPtr, aLen, err := g.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	bPtr, bLen, err := g.stringRegsOf(ins.Src2)
	if err != nil {
		return err
	}
	g.emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen)
	if err := g.emitCallFunctionValue(funcval); err != nil {
		return err
	}
	if dstPtr != 0 {
		g.emitMovFromAsmReg(dstPtr, asmx86.REG_AX)
	}
	if asmIntReg(dstLen) != asmx86.REG_BX {
		g.emitMovFromAsmReg(dstLen, asmx86.REG_BX)
	}
	return nil
}

// emitCallOneStringToInt emits string -> int64 helper call:
// move args to ABI int arg regs, CALL fn, result in AX.
func (g *assembler) emitCallOneStringToInt(ins Instr, funcval uintptr) error {
	dst, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	aPtr, aLen, err := g.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	g.emitMoveStringToArgs(0, aPtr, aLen)
	if err := g.emitCallFunctionValue(funcval); err != nil {
		return err
	}
	if dst != 0 {
		g.emitMovFromAsmReg(dst, asmx86.REG_AX)
	}
	return nil
}

// emitCallSliceToBool emits (string, *sliceHeader) -> bool helper call:
// AX/BX=needle ptr/len, CX=list field address, CALL fn, bool result in AL/AX.
func (g *assembler) emitCallSliceToBool(ins Instr, funcval uintptr) error {
	dst, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	needlePtr, needleLen, err := g.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	listAddr, err := g.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	g.emitParallelIntMoves([][2]int16{
		{asmArgReg(0), asmIntReg(needlePtr)},
		{asmArgReg(1), asmIntReg(needleLen)},
		{asmArgReg(2), asmIntReg(listAddr)},
	})
	if err := g.emitCallFunctionValue(funcval); err != nil {
		return err
	}
	g.emitMovBoolFromAX(dst)
	return nil
}

// emitCallArrayToBool emits (string, *array, len) -> bool helper call:
// AX/BX=needle ptr/len, CX=array address, DI=len, CALL fn, bool result in AL/AX.
func (g *assembler) emitCallArrayToBool(ins Instr, funcval uintptr) error {
	if ins.Imm < 0 {
		return fmt.Errorf("%w: array length must be non-negative, got %d", ErrCodegenUnsupported, ins.Imm)
	}
	dst, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	needlePtr, needleLen, err := g.stringRegsOf(ins.Src1)
	if err != nil {
		return err
	}
	arrayAddr, err := g.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	g.emitLoadImm(regIntTmp, uint64(ins.Imm))
	g.emitParallelIntMoves([][2]int16{
		{asmArgReg(0), asmIntReg(needlePtr)},
		{asmArgReg(1), asmIntReg(needleLen)},
		{asmArgReg(2), asmIntReg(arrayAddr)},
		{asmArgReg(3), asmIntReg(regIntTmp)},
	})
	if err := g.emitCallFunctionValue(funcval); err != nil {
		return err
	}
	g.emitMovBoolFromAX(dst)
	return nil
}

// emitMoveStringToArgs moves one string pair to ABI argument registers:
// arg[argBase]=ptr, arg[argBase+1]=len.
func (g *assembler) emitMoveStringToArgs(argBase, ptrReg, lenReg uint32) {
	g.emitParallelIntMoves([][2]int16{
		{asmArgReg(int(argBase)), asmIntReg(ptrReg)},
		{asmArgReg(int(argBase + 1)), asmIntReg(lenReg)},
	})
}

// emitMoveTwoStringsToArgs moves two string pairs to ABI argument registers:
// arg0/1=lhs ptr/len, arg2/3=rhs ptr/len.
func (g *assembler) emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen uint32) {
	g.emitParallelIntMoves([][2]int16{
		{asmArgReg(0), asmIntReg(aPtr)},
		{asmArgReg(1), asmIntReg(aLen)},
		{asmArgReg(2), asmIntReg(bPtr)},
		{asmArgReg(3), asmIntReg(bLen)},
	})
}

// emitParallelIntMoves performs parallel register moves through stack spills:
// MOVQ src_i, [SP+off_i] then MOVQ [SP+off_i], dst_i.
func (g *assembler) emitParallelIntMoves(moves [][2]int16) {
	if len(moves) == 0 {
		return
	}
	count := 0
	for _, m := range moves {
		if m[0] == m[1] {
			continue
		}
		off := int32(count * 8)
		g.emitStoreMemFromAsmReg(m[1], asmx86.REG_SP, off)
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
		g.emitLoadMemToAsmReg(m[0], asmx86.REG_SP, off)
		count++
	}
}

// emitCallFunctionValue emits a function-value indirect call sequence:
// MOVQ $funcval, R13; MOVQ R13, DX; MOVQ (R13), R13; CALL R13.
func (g *assembler) emitCallFunctionValue(funcval uintptr) error {
	if funcval == 0 {
		return fmt.Errorf("%w: helper function pointer is nil", ErrCodegenUnsupported)
	}
	g.emitLoadImm(regIntTmp, uint64(funcval))
	// RDX is the closure context register under ABIInternal.
	g.emitMovReg(2, regIntTmp)
	g.emitLoadMem64(regIntTmp, asmIntReg(regIntTmp), 0)
	g.emitCallReg(regIntTmp)
	return nil
}

// emitMove emits typed MOVE lowering:
// MOVSD for float, two MOVQ for string pair, MOVQ for integer/bool.
func (g *assembler) emitMove(ins Instr) error {
	switch ins.Type {
	case T_FLOAT64:
		fd, err := g.floatRegOf(ins.Dst)
		if err != nil {
			return err
		}
		fs, err := g.floatRegOf(ins.Src1)
		if err != nil {
			return err
		}
		g.emitMoveFloatToFloat(fd, fs)
		return nil
	case T_STRING:
		d0, d1, err := g.stringRegsOf(ins.Dst)
		if err != nil {
			return err
		}
		s0, s1, err := g.stringRegsOf(ins.Src1)
		if err != nil {
			return err
		}
		g.emitMovReg(d0, s0)
		g.emitMovReg(d1, s1)
		return nil
	default:
		rd, err := g.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		rs, err := g.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		g.emitMovReg(rd, rs)
		return nil
	}
}

// emitBinary emits integer binary op in two-address form:
// MOVQ dst, src1; OP dst, src2.
func (g *assembler) emitBinary(ins Instr, as obj.As) error {
	rd, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := g.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := g.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	if rd != rn {
		g.emitMovReg(rd, rn)
	}
	p := g.asmCtxt.NewProg()
	p.As = as
	p.From = regAddr(asmIntReg(rm))
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
	return nil
}

// emitMulInt emits integer multiply:
// IMULQ Rdst, Rsrc2 (after moving src1 into dst).
func (g *assembler) emitMulInt(ins Instr) error {
	return g.emitBinary(ins, asmx86.AIMULQ)
}

// emitDiv emits signed/unsigned integer divide:
// AX=src1; CQO/XOR DX,DX; IDIVQ/DIVQ src2; quotient in AX.
func (g *assembler) emitDiv(ins Instr, signed bool) error {
	rd, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := g.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := g.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	divisor := rm
	if divisor == 0 || divisor == 2 {
		g.emitMovReg(regIntTmp, divisor)
		divisor = regIntTmp
	}
	if rn != 0 {
		g.emitMovReg(0, rn)
	}
	if signed {
		p := g.asmCtxt.NewProg()
		p.As = asmx86.ACQO
		g.registerProg(p)
	} else {
		p := g.asmCtxt.NewProg()
		p.As = asmx86.AXORQ
		p.From = regAddr(asmx86.REG_DX)
		p.To = regAddr(asmx86.REG_DX)
		g.registerProg(p)
	}
	p := g.asmCtxt.NewProg()
	if signed {
		p.As = asmx86.AIDIVQ
	} else {
		p.As = asmx86.ADIVQ
	}
	p.From = regAddr(asmIntReg(divisor))
	g.registerProg(p)
	if rd != 0 {
		g.emitMovReg(rd, 0)
	}
	return nil
}

// emitMod emits signed/unsigned integer remainder:
// AX=src1; CQO/XOR DX,DX; IDIVQ/DIVQ src2; remainder in DX.
func (g *assembler) emitMod(ins Instr, signed bool) error {
	rd, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := g.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := g.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	divisor := rm
	if divisor == 0 || divisor == 2 {
		g.emitMovReg(regIntTmp, divisor)
		divisor = regIntTmp
	}
	if rn != 0 {
		g.emitMovReg(0, rn)
	}
	if signed {
		p := g.asmCtxt.NewProg()
		p.As = asmx86.ACQO
		g.registerProg(p)
	} else {
		p := g.asmCtxt.NewProg()
		p.As = asmx86.AXORQ
		p.From = regAddr(asmx86.REG_DX)
		p.To = regAddr(asmx86.REG_DX)
		g.registerProg(p)
	}
	p := g.asmCtxt.NewProg()
	if signed {
		p.As = asmx86.AIDIVQ
	} else {
		p.As = asmx86.ADIVQ
	}
	p.From = regAddr(asmIntReg(divisor))
	g.registerProg(p)
	if rd != 2 {
		g.emitMovReg(rd, 2)
	}
	return nil
}

// emitNegInt emits integer negate:
// MOVQ dst, src; NEGQ dst.
func (g *assembler) emitNegInt(ins Instr) error {
	rd, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rs, err := g.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	if rd != rs {
		g.emitMovReg(rd, rs)
	}
	p := g.asmCtxt.NewProg()
	p.As = asmx86.ANEGQ
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
	return nil
}

// emitBinaryFloat emits float binary op in two-address form:
// MOVSD dst, src1; FOP dst, src2.
func (g *assembler) emitBinaryFloat(ins Instr, as obj.As) error {
	fd, err := g.floatRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fn, err := g.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	fm, err := g.floatRegOf(ins.Src2)
	if err != nil {
		return err
	}
	if fd != fn {
		g.emitMoveFloatToFloat(fd, fn)
	}
	p := g.asmCtxt.NewProg()
	p.As = as
	p.From = regAddr(asmFloatReg(fm))
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
	return nil
}

// emitNegFloat emits float negate by toggling sign bit:
// MOVQ tmp, src; BTCQ tmp, 63; MOVQ dst, tmp.
func (g *assembler) emitNegFloat(ins Instr) error {
	fd, err := g.floatRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fs, err := g.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	g.emitMovFloatToInt(regIntTmp, fs)
	p := g.asmCtxt.NewProg()
	p.As = asmx86.ABTCQ
	p.From = obj.Addr{Type: obj.TYPE_CONST, Offset: 63}
	p.To = regAddr(asmIntReg(regIntTmp))
	g.registerProg(p)
	g.emitMovIntToFloat(fd, regIntTmp)
	return nil
}

// emitCompareSetBool emits integer compare+set:
// CMPQ src1, src2; SETcc dst8; MOVBQZX dst, dst8.
func (g *assembler) emitCompareSetBool(ins Instr, setAs obj.As) error {
	rd, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rn, err := g.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	rm, err := g.intRegOf(ins.Src2)
	if err != nil {
		return err
	}
	g.emitCmpReg(rn, rm)
	return g.emitSetBool(rd, setAs)
}

// emitCompareSetBoolFloat emits float compare+set:
// UCOMISD src1, src2; SETcc handling includes unordered (NaN) semantics.
func (g *assembler) emitCompareSetBoolFloat(ins Instr, setAs obj.As) error {
	rd, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fn, err := g.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	fm, err := g.floatRegOf(ins.Src2)
	if err != nil {
		return err
	}
	g.emitFcmpReg(fn, fm)
	switch setAs {
	case asmx86.ASETEQ:
		g.emitSetBoolByte(rd, asmx86.ASETEQ)
		g.emitSetBoolByte(regIntTmp, asmx86.ASETPC)
		g.emitAndByte(rd, regIntTmp)
		g.emitMovBoolByteToInt(rd)
		return nil
	case asmx86.ASETNE:
		g.emitSetBoolByte(rd, asmx86.ASETNE)
		g.emitSetBoolByte(regIntTmp, asmx86.ASETPS)
		g.emitOrByte(rd, regIntTmp)
		g.emitMovBoolByteToInt(rd)
		return nil
	case asmx86.ASETCS, asmx86.ASETLS:
		g.emitSetBoolByte(rd, setAs)
		g.emitSetBoolByte(regIntTmp, asmx86.ASETPC)
		g.emitAndByte(rd, regIntTmp)
		g.emitMovBoolByteToInt(rd)
		return nil
	default:
		return g.emitSetBool(rd, setAs)
	}
}

// emitSetBool materializes a SETcc result as full-width integer bool.
func (g *assembler) emitSetBool(rd uint32, setAs obj.As) error {
	g.emitSetBoolByte(rd, setAs)
	g.emitMovBoolByteToInt(rd)
	return nil
}

// emitSetBoolByte emits SETcc into an 8-bit destination register.
func (g *assembler) emitSetBoolByte(rd uint32, setAs obj.As) {
	p := g.asmCtxt.NewProg()
	p.As = setAs
	p.To = regAddr(asmInt8Reg(asmIntReg(rd)))
	g.registerProg(p)
}

// emitMovBoolByteToInt zero-extends 8-bit bool into 64-bit register.
func (g *assembler) emitMovBoolByteToInt(rd uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVBQZX
	p.From = regAddr(asmInt8Reg(asmIntReg(rd)))
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitAndByte emits 8-bit AND: dst8 &= src8.
func (g *assembler) emitAndByte(dst, src uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AANDB
	p.From = regAddr(asmInt8Reg(asmIntReg(src)))
	p.To = regAddr(asmInt8Reg(asmIntReg(dst)))
	g.registerProg(p)
}

// emitOrByte emits 8-bit OR: dst8 |= src8.
func (g *assembler) emitOrByte(dst, src uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AORB
	p.From = regAddr(asmInt8Reg(asmIntReg(src)))
	p.To = regAddr(asmInt8Reg(asmIntReg(dst)))
	g.registerProg(p)
}

// emitCmpReg emits integer compare:
// CMPQ Rrn, Rrm.
func (g *assembler) emitCmpReg(rn, rm uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.ACMPQ
	p.From = regAddr(asmIntReg(rn))
	p.To = regAddr(asmIntReg(rm))
	g.registerProg(p)
}

// emitMovBoolFromAX moves AL bool result to a full-width destination register.
func (g *assembler) emitMovBoolFromAX(dst uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVBQZX
	p.From = regAddr(asmx86.REG_AL)
	p.To = regAddr(asmIntReg(dst))
	g.registerProg(p)
}

// emitFcmpReg emits floating compare:
// UCOMISD Xfn, Xfm.
func (g *assembler) emitFcmpReg(fn, fm uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AUCOMISD
	p.From = regAddr(asmFloatReg(fm))
	p.To = regAddr(asmFloatReg(fn))
	g.registerProg(p)
}

// emitBranch emits JMP/Jcc with unresolved TYPE_BRANCH target.
func (g *assembler) emitBranch(ins Instr, as obj.As) error {
	p := g.asmCtxt.NewProg()
	p.As = as
	p.To.Type = obj.TYPE_BRANCH
	g.registerProg(p)
	g.registerBranch(int(ins.Lbl), p)
	return nil
}

// emitTestReg emits integer test:
// TESTQ Rrn, Rrn.
func (g *assembler) emitTestReg(rn uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.ATESTQ
	p.From = regAddr(asmIntReg(rn))
	p.To = regAddr(asmIntReg(rn))
	g.registerProg(p)
}

// emitMovReg emits integer register move:
// MOVQ Rrd, Rrs.
func (g *assembler) emitMovReg(rd, rs uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitMovFromAsmReg emits integer move from raw asm register to virtual dst register.
func (g *assembler) emitMovFromAsmReg(dst uint32, src int16) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(src)
	p.To = regAddr(asmIntReg(dst))
	g.registerProg(p)
}

// emitMoveFloatToFloat emits float register move:
// MOVSD Xfd, Xfs.
func (g *assembler) emitMoveFloatToFloat(fd, fs uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVSD
	p.From = regAddr(asmFloatReg(fs))
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
}

// emitMovIntToFloat emits integer->float register bit-move:
// MOVQ Xfd, Rrs.
func (g *assembler) emitMovIntToFloat(fd, rs uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
}

// emitMovFloatToInt emits float->integer register bit-move:
// MOVQ Rrd, Xfs.
func (g *assembler) emitMovFloatToInt(rd, fs uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmFloatReg(fs))
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitLoadImm emits constant materialization:
// MOVQ Rrd, $imm.
func (g *assembler) emitLoadImm(rd uint32, imm uint64) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = obj.Addr{Type: obj.TYPE_CONST, Offset: int64(imm)}
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitLoadInputBase reloads input pointer shadow from stack frame.
func (g *assembler) emitLoadInputBase(rd uint32) {
	g.emitLoadMemToAsmReg(asmIntReg(rd), asmx86.REG_SP, amd64InputBaseOff)
}

// emitLoadFieldAddr emits field address materialization:
// Rrd = input_base + offset.
func (g *assembler) emitLoadFieldAddr(rd uint32, offset int32) {
	g.emitLoadInputBase(rd)
	if offset == 0 {
		return
	}
	p := g.asmCtxt.NewProg()
	p.As = asmx86.ALEAQ
	p.From = memAddr(asmIntReg(rd), offset)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitLoadMem64 emits 64-bit load:
// MOVQ Rrd, [base+off].
func (g *assembler) emitLoadMem64(rd uint32, base int16, off int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = memAddr(base, off)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitStoreMem64 emits 64-bit store:
// MOVQ [base+off], Rrs.
func (g *assembler) emitStoreMem64(rs uint32, base int16, off int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(asmIntReg(rs))
	p.To = memAddr(base, off)
	g.registerProg(p)
}

// emitLoadMemBool emits bool load with zero-extension:
// MOVBQZX Rrd, byte [base+off].
func (g *assembler) emitLoadMemBool(rd uint32, base int16, off int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVBQZX
	p.From = memAddr(base, off)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitLoadMemFloat emits float64 load:
// MOVSD Xfd, [base+off].
func (g *assembler) emitLoadMemFloat(fd uint32, base int16, off int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVSD
	p.From = memAddr(base, off)
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
}

// emitLoadMemToAsmReg emits 64-bit load into raw asm register:
// MOVQ dst, [base+off].
func (g *assembler) emitLoadMemToAsmReg(dst int16, base int16, off int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = memAddr(base, off)
	p.To = regAddr(dst)
	g.registerProg(p)
}

// emitStoreMemFromAsmReg emits 64-bit store from raw asm register:
// MOVQ [base+off], src.
func (g *assembler) emitStoreMemFromAsmReg(src int16, base int16, off int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AMOVQ
	p.From = regAddr(src)
	p.To = memAddr(base, off)
	g.registerProg(p)
}

// emitAddImmSP emits stack pointer increment:
// ADDQ SP, #imm.
func (g *assembler) emitAddImmSP(imm int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.AADDQ
	p.From = obj.Addr{Type: obj.TYPE_CONST, Offset: int64(imm)}
	p.To = regAddr(asmx86.REG_SP)
	g.registerProg(p)
}

// emitSubImmSP emits stack pointer decrement:
// SUBQ SP, #imm.
func (g *assembler) emitSubImmSP(imm int32) {
	p := g.asmCtxt.NewProg()
	p.As = asmx86.ASUBQ
	p.From = obj.Addr{Type: obj.TYPE_CONST, Offset: int64(imm)}
	p.To = regAddr(asmx86.REG_SP)
	g.registerProg(p)
}

// emitCallReg emits indirect call:
// CALL Rreg.
func (g *assembler) emitCallReg(reg uint32) {
	p := g.asmCtxt.NewProg()
	p.As = obj.ACALL
	p.To = regAddr(asmIntReg(reg))
	g.registerProg(p)
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
