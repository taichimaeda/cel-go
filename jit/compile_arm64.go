//go:build arm64

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
	asmarm64 "github.com/twitchyliquid64/golang-asm/obj/arm64"
	"github.com/twitchyliquid64/golang-asm/objabi"
)

const (
	regArgInput     = 0
	regInputScratch = 19
	regRet          = 0
	regIntTmp       = 17
)

const (
	// Go's internal ABI may spill register arguments of called functions into the
	// caller's outgoing-arg area at SP+8, SP+24, etc. Reserve that area so helper
	// calls cannot clobber our saved registers (especially LR).
	arm64CallAreaBytes = 264

	// Runtime unwinding on LR architectures expects return PC at SP+0.
	arm64SaveX30Off = 0

	// Keep outgoing arg spill area at low offsets above SP.
	arm64SaveX19Off = 8 + arm64CallAreaBytes
	arm64SaveX20Off = arm64SaveX19Off + 8
	arm64SaveX21Off = arm64SaveX20Off + 8
	arm64SaveX22Off = arm64SaveX21Off + 8
	arm64SaveX23Off = arm64SaveX22Off + 8
	arm64SaveX24Off = arm64SaveX23Off + 8
	arm64SaveX25Off = arm64SaveX24Off + 8

	// Keep old FP at frameBytes-8 to match Go frame-pointer expectations.
	arm64SaveX29Off = arm64SaveX25Off + 8
	// Mirror LR next to FP for frame-pointer-based stack walking.
	arm64SaveX30FpOff = arm64SaveX29Off + 8
	// Keep a stable copy of the input pointer in-frame since arm64 internal ABI
	// treats R19-R25 as scratch across calls.
	arm64InputBaseOff = arm64SaveX30FpOff + 8
	// Keep SP 16-byte aligned.
	arm64FrameBytes = arm64SaveX30FpOff + 16

	// Temporary area in the caller spill region used for parallel argument moves.
	arm64ArgMoveSpillOff = 8
	nativeFrameBytes     = arm64FrameBytes
)

func newNativeAsmContext() (*obj.Link, *arch.Arch) {
	asmArch := arch.Set("arm64")
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
//	high address
//	+-------------------------------+
//	| input base ptr [SP+arm64InputBaseOff]
//	+-------------------------------+
//	| mirrored LR      [SP+arm64SaveX30FpOff]
//	+-------------------------------+
//	| saved FP (X29)   [SP+arm64SaveX29Off]
//	+-------------------------------+
//	| saved X25..X19   [SP+arm64SaveX25Off ... arm64SaveX19Off]
//	+-------------------------------+
//	| call-area spill region (helper-call ABI scratch)
//	|                 [SP+8 ... SP+8+arm64CallAreaBytes)
//	+-------------------------------+
//	| saved LR (X30)   [SP+arm64SaveX30Off = SP+0]
//	+-------------------------------+  <- SP
//	low address

// emitPrologue emits frame setup and callee-saved spills:
// SUB SP, SP, #frame; STR X19..X30, [SP+off].
func (g *assembler) emitPrologue() {
	// Preserve LR plus callee-saved registers X19..X25.
	// Keep low frame offsets available for outgoing arg spills.
	g.emitSubImm12(31, 31, arm64FrameBytes)
	g.emitStrUnsignedImm64(30, 31, arm64SaveX30Off)
	g.emitStrUnsignedImm64(19, 31, arm64SaveX19Off)
	g.emitStrUnsignedImm64(20, 31, arm64SaveX20Off)
	g.emitStrUnsignedImm64(21, 31, arm64SaveX21Off)
	g.emitStrUnsignedImm64(22, 31, arm64SaveX22Off)
	g.emitStrUnsignedImm64(23, 31, arm64SaveX23Off)
	g.emitStrUnsignedImm64(24, 31, arm64SaveX24Off)
	g.emitStrUnsignedImm64(25, 31, arm64SaveX25Off)
	g.emitStrUnsignedImm64(29, 31, arm64SaveX29Off)
	g.emitStrUnsignedImm64(30, 31, arm64SaveX30FpOff)
	// Keep a canonical frame record for Go unwinding:
	// [FP+0] = previous FP, [FP+8] = LR.
	g.emitAddImm12(29, 31, arm64SaveX29Off)
	g.emitStrUnsignedImm64(regArgInput, 31, arm64InputBaseOff)
}

// emitEpilogue emits callee-saved restores and return:
// LDR X19..X30, [SP+off]; ADD SP, SP, #frame; RET.
func (g *assembler) emitEpilogue() {
	// Restore callee-saved state and return.
	g.emitLdrUnsignedImm64(29, 31, arm64SaveX29Off)
	g.emitLdrUnsignedImm64(25, 31, arm64SaveX25Off)
	g.emitLdrUnsignedImm64(24, 31, arm64SaveX24Off)
	g.emitLdrUnsignedImm64(23, 31, arm64SaveX23Off)
	g.emitLdrUnsignedImm64(22, 31, arm64SaveX22Off)
	g.emitLdrUnsignedImm64(21, 31, arm64SaveX21Off)
	g.emitLdrUnsignedImm64(20, 31, arm64SaveX20Off)
	g.emitLdrUnsignedImm64(19, 31, arm64SaveX19Off)
	g.emitLdrUnsignedImm64(30, 31, arm64SaveX30Off)
	g.emitAddImm12(31, 31, arm64FrameBytes)
	p := g.asmCtxt.NewProg()
	p.As = obj.ARET
	p.To = regAddr(asmarm64.REG_R30)
	g.registerProg(p)
}

// emitInstr lowers one IR instruction to one or more ARM64 instructions.
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
		offset := uint64(ins.Imm)
		switch ins.Type {
		case T_INT64, T_UINT64:
			rd, err := g.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			g.emitLdrUnsignedImm64(rd, regIntTmp, 0)
			return nil
		case T_BOOL:
			rd, err := g.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			g.emitLdrbUnsignedImm32(rd, regIntTmp, 0)
			return nil
		case T_FLOAT64:
			fd, err := g.floatRegOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			g.emitLdrUnsignedImm64(regIntTmp, regIntTmp, 0)
			g.emitMovIntToFloat(fd, regIntTmp)
			return nil
		case T_STRING:
			rdPtr, rdLen, err := g.stringRegsOf(ins.Dst)
			if err != nil {
				return err
			}
			g.emitLoadFieldAddr(regIntTmp, offset)
			p := g.asmCtxt.NewProg()
			p.As = asmarm64.ALDP
			p.From = obj.Addr{
				Type: obj.TYPE_MEM,
				Reg:  asmIntReg(regIntTmp),
			}
			p.To = obj.Addr{
				Type:   obj.TYPE_REGREG,
				Reg:    asmIntReg(rdPtr),
				Offset: int64(asmIntReg(rdLen)),
			}
			g.registerProg(p)
			return nil
		default:
			return fmt.Errorf("%w: LOAD_FIELD type %v is unsupported on arm64", ErrCodegenUnsupported, ins.Type)
		}
	case LOAD_FIELD_SLICE, LOAD_FIELD_ARRAY:
		if ins.Imm < 0 {
			return fmt.Errorf("%w: %v offset must be non-negative, got %d", ErrCodegenUnsupported, ins.Op, ins.Imm)
		}
		rd, err := g.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		g.emitLoadFieldAddr(rd, uint64(ins.Imm))
		return nil
	case ADD_INT, ADD_UINT:
		return g.emitBinary(ins, asmarm64.AADD)
	case SUB_INT, SUB_UINT:
		return g.emitBinary(ins, asmarm64.ASUB)
	case MUL_INT, MUL_UINT:
		return g.emitBinary(ins, asmarm64.AMUL)
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
		return g.emitBinaryFloat(ins, asmarm64.AFADDD)
	case SUB_FLOAT:
		return g.emitBinaryFloat(ins, asmarm64.AFSUBD)
	case MUL_FLOAT:
		return g.emitBinaryFloat(ins, asmarm64.AFMULD)
	case DIV_FLOAT:
		return g.emitBinaryFloat(ins, asmarm64.AFDIVD)
	case NEG_FLOAT:
		return g.emitNegFloat(ins)
	case EQ_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmarm64.COND_EQ)
	case NE_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmarm64.COND_NE)
	case GT_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmarm64.COND_GT)
	case GE_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmarm64.COND_GE)
	case LT_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmarm64.COND_LT)
	case LE_FLOAT:
		return g.emitCompareSetBoolFloat(ins, asmarm64.COND_LE)
	case EQ_INT, EQ_UINT:
		return g.emitCompareSetBool(ins, asmarm64.COND_EQ)
	case NE_INT, NE_UINT:
		return g.emitCompareSetBool(ins, asmarm64.COND_NE)
	case GT_INT:
		return g.emitCompareSetBool(ins, asmarm64.COND_GT)
	case GE_INT:
		return g.emitCompareSetBool(ins, asmarm64.COND_GE)
	case LT_INT:
		return g.emitCompareSetBool(ins, asmarm64.COND_LT)
	case LE_INT:
		return g.emitCompareSetBool(ins, asmarm64.COND_LE)
	case LT_UINT:
		return g.emitCompareSetBool(ins, asmarm64.COND_LO)
	case LE_UINT:
		return g.emitCompareSetBool(ins, asmarm64.COND_LS)
	case GT_UINT:
		return g.emitCompareSetBool(ins, asmarm64.COND_HI)
	case GE_UINT:
		return g.emitCompareSetBool(ins, asmarm64.COND_HS)
	case MOVE:
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
	case NOT:
		rd, err := g.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		rs, err := g.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		p := g.asmCtxt.NewProg()
		p.As = asmarm64.ACMP
		p.From = regAddr(asmarm64.REGZERO)
		p.Reg = asmIntReg(rs)
		g.registerProg(p)
		return g.emitSetBool(rd, asmarm64.COND_EQ)
	case BR:
		return g.emitBranch(ins, asmarm64.AB, 0)
	case BR_TRUE:
		return g.emitBranch(ins, asmarm64.ACBNZ, ins.Src1)
	case BR_FALSE:
		return g.emitBranch(ins, asmarm64.ACBZ, ins.Src1)
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
		return fmt.Errorf("%w: opcode %v is unsupported on arm64", ErrCodegenUnsupported, ins.Op)
	}
}

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
		return g.emitCallListSliceToBool(ins, funcValue)
	case BuiltinListContainsStringArray:
		return g.emitCallListArrayToBool(ins, funcValue)
	default:
		return fmt.Errorf("%w: builtin %v is unsupported on arm64", ErrCodegenUnsupported, ins.BuiltinID)
	}
}

// emitBranch emits B/CBZ/CBNZ with an unresolved TYPE_BRANCH target.
func (g *assembler) emitBranch(ins Instr, as obj.As, src VReg) error {
	p := g.asmCtxt.NewProg()
	p.As = as
	if src != 0 {
		rs, err := g.intRegOf(src)
		if err != nil {
			return err
		}
		p.From = regAddr(asmIntReg(rs))
	}
	p.To.Type = obj.TYPE_BRANCH
	g.registerProg(p)
	g.registerBranch(int(ins.Lbl), p)
	return nil
}

// emitCallTwoStringToBool emits string,string -> bool helper call:
// MOV args (X0..X3), BLR fn, result in X0.
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
	if dst != 0 {
		g.emitMovReg(dst, 0)
	}
	return nil
}

// emitCallTwoStringToString emits string,string -> string helper call:
// MOV args (X0..X3), BLR fn, result in (X0,X1).
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
		g.emitMovReg(dstPtr, 0)
	}
	if dstLen != 1 {
		g.emitMovReg(dstLen, 1)
	}
	return nil
}

// emitCallOneStringToInt emits string -> int64 helper call:
// MOV args (X0,X1), BLR fn, result in X0.
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
		g.emitMovReg(dst, 0)
	}
	return nil
}

// emitCallListSliceToBool emits (string, *sliceHeader) -> bool helper call:
// X0/X1=needle ptr/len, X2=list field address, BLR fn, result in X0.
func (g *assembler) emitCallListSliceToBool(ins Instr, funcval uintptr) error {
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
	g.emitParallelIntMoves([][2]uint32{
		{0, needlePtr},
		{1, needleLen},
		{2, listAddr},
	})
	if err := g.emitCallFunctionValue(funcval); err != nil {
		return err
	}
	if dst != 0 {
		g.emitMovReg(dst, 0)
	}
	return nil
}

// emitCallListArrayToBool emits (string, *array, len) -> bool helper call:
// X0/X1=needle ptr/len, X2=array address, X3=len, BLR fn, result in X0.
func (g *assembler) emitCallListArrayToBool(ins Instr, funcval uintptr) error {
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
	g.emitParallelIntMoves([][2]uint32{
		{0, needlePtr},
		{1, needleLen},
		{2, arrayAddr},
		{3, regIntTmp},
	})
	if err := g.emitCallFunctionValue(funcval); err != nil {
		return err
	}
	if dst != 0 {
		g.emitMovReg(dst, 0)
	}
	return nil
}

// emitMoveStringToArgs moves one string pair to ABI argument registers:
// X{argBase}=ptr, X{argBase+1}=len.
func (g *assembler) emitMoveStringToArgs(argBase, ptrReg, lenReg uint32) {
	g.emitParallelIntMoves([][2]uint32{
		{argBase, ptrReg},
		{argBase + 1, lenReg},
	})
}

// emitMoveTwoStringsToArgs moves two string pairs to ABI argument registers:
// X0/X1=lhs ptr/len, X2/X3=rhs ptr/len.
func (g *assembler) emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen uint32) {
	g.emitParallelIntMoves([][2]uint32{
		{0, aPtr},
		{1, aLen},
		{2, bPtr},
		{3, bLen},
	})
}

// emitParallelIntMoves performs parallel register moves through stack spills:
// STR src_i, [SP+off_i] then LDR dst_i, [SP+off_i].
func (g *assembler) emitParallelIntMoves(moves [][2]uint32) {
	if len(moves) == 0 {
		return
	}
	count := 0
	for _, m := range moves {
		if m[0] == m[1] {
			continue
		}
		off := uint32(arm64ArgMoveSpillOff + count*8)
		g.emitStrUnsignedImm64(m[1], 31, off)
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
		off := uint32(arm64ArgMoveSpillOff + count*8)
		g.emitLdrUnsignedImm64(m[0], 31, off)
		count++
	}
}

// emitCallFunctionValue emits a function-value indirect call sequence:
// MOVD $funcval, X17; MOVD X17, X26; MOVD (X17), X17; BLR X17.
func (g *assembler) emitCallFunctionValue(funcval uintptr) error {
	if funcval == 0 {
		return fmt.Errorf("%w: helper function pointer is nil", ErrCodegenUnsupported)
	}
	g.emitLoadImm(regIntTmp, uint64(funcval))
	g.emitMovReg(26, regIntTmp)
	g.emitLdrUnsignedImm64(regIntTmp, regIntTmp, 0)
	g.emitBlr(regIntTmp)
	return nil
}

// emitBinary emits a 2-operand integer ALU op in 3-register form:
// OP X(ins.Dst), X(ins.Src1), X(ins.Src2).
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
	p := g.asmCtxt.NewProg()
	p.As = as
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
	return nil
}

// emitDiv emits signed/unsigned integer divide:
// SDIV/UDIV X(ins.Dst), X(ins.Src1), X(ins.Src2).
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
	p := g.asmCtxt.NewProg()
	if signed {
		p.As = asmarm64.ASDIV
	} else {
		p.As = asmarm64.AUDIV
	}
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
	return nil
}

// emitMod emits integer remainder via:
// Xquotient = SDIV/UDIV Xrn,Xrm; Xquotient = MUL Xquotient,Xrm; Xrd = SUB Xrn,Xquotient.
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
	quotient := uint32(regIntTmp)
	div := g.asmCtxt.NewProg()
	if signed {
		div.As = asmarm64.ASDIV
	} else {
		div.As = asmarm64.AUDIV
	}
	div.From = regAddr(asmIntReg(rm))
	div.Reg = asmIntReg(rn)
	div.To = regAddr(asmIntReg(quotient))
	g.registerProg(div)
	// quotient = quotient * rm
	mul := g.asmCtxt.NewProg()
	mul.As = asmarm64.AMUL
	mul.From = regAddr(asmIntReg(rm))
	mul.Reg = asmIntReg(quotient)
	mul.To = regAddr(asmIntReg(quotient))
	g.registerProg(mul)
	// rd = rn - quotient
	sub := g.asmCtxt.NewProg()
	sub.As = asmarm64.ASUB
	sub.From = regAddr(asmIntReg(quotient))
	sub.Reg = asmIntReg(rn)
	sub.To = regAddr(asmIntReg(rd))
	g.registerProg(sub)
	return nil
}

// emitNegInt emits integer negate:
// NEG X(ins.Dst), X(ins.Src1).
func (g *assembler) emitNegInt(ins Instr) error {
	rd, err := g.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rs, err := g.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.ANEG
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
	return nil
}

// emitBinaryFloat emits 2-operand float ALU op:
// FOP D(ins.Dst), D(ins.Src1), D(ins.Src2).
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
	p := g.asmCtxt.NewProg()
	p.As = as
	p.From = regAddr(asmFloatReg(fm))
	p.Reg = asmFloatReg(fn)
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
	return nil
}

// emitNegFloat emits float negate:
// FNEG D(ins.Dst), D(ins.Src1).
func (g *assembler) emitNegFloat(ins Instr) error {
	fd, err := g.floatRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fn, err := g.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AFNEGD
	p.From = regAddr(asmFloatReg(fn))
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
	return nil
}

// emitCompareSetBool emits integer compare+set:
// CMP X(ins.Src1), X(ins.Src2); CSET X(ins.Dst), cond.
func (g *assembler) emitCompareSetBool(ins Instr, cond int16) error {
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
	return g.emitSetBool(rd, cond)
}

// emitCompareSetBoolFloat emits float compare+set:
// FCMP D(ins.Src1), D(ins.Src2); CSET X(ins.Dst), cond.
func (g *assembler) emitCompareSetBoolFloat(ins Instr, cond int16) error {
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
	return g.emitSetBool(rd, cond)
}

// emitSetBool emits boolean materialization:
// CSET Xrd, cond.
func (g *assembler) emitSetBool(rd uint32, cond int16) error {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.ACSET
	p.From = regAddr(cond)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
	return nil
}

// emitCmpReg emits integer compare:
// CMP Xrn, Xrm.
func (g *assembler) emitCmpReg(rn, rm uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.ACMP
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	g.registerProg(p)
}

// emitMovReg emits integer register move:
// MOVD Xrd, Xrn.
func (g *assembler) emitMovReg(rd, rn uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = regAddr(asmIntReg(rn))
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitBlr emits indirect call:
// BLR Xrn.
func (g *assembler) emitBlr(rn uint32) {
	p := g.asmCtxt.NewProg()
	p.As = obj.ACALL
	p.To = regAddr(asmIntReg(rn))
	g.registerProg(p)
}

// emitMovIntToFloat emits integer->float register bit-move:
// FMOV Dfd, Xrn.
func (g *assembler) emitMovIntToFloat(fd, rn uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AFMOVD
	p.From = regAddr(asmIntReg(rn))
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
}

// emitMoveFloatToFloat emits float register move:
// FMOV Dfd, Dfn.
func (g *assembler) emitMoveFloatToFloat(fd, fn uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AFMOVD
	p.From = regAddr(asmFloatReg(fn))
	p.To = regAddr(asmFloatReg(fd))
	g.registerProg(p)
}

// emitFcmpReg emits float compare:
// FCMP Dfn, Dfm.
func (g *assembler) emitFcmpReg(fn, fm uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AFCMPD
	p.From = regAddr(asmFloatReg(fm))
	p.Reg = asmFloatReg(fn)
	g.registerProg(p)
}

// emitLdrUnsignedImm64 emits 64-bit load:
// LDR Xrt, [Xrn, #byteOffset].
func (g *assembler) emitLdrUnsignedImm64(rt, rn, byteOffset uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(byteOffset),
	}
	p.To = regAddr(asmIntReg(rt))
	g.registerProg(p)
}

// emitStrUnsignedImm64 emits 64-bit store:
// STR Xrt, [Xrn, #byteOffset].
func (g *assembler) emitStrUnsignedImm64(rt, rn, byteOffset uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = regAddr(asmIntReg(rt))
	p.To = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(byteOffset),
	}
	g.registerProg(p)
}

// emitSubImm12 emits immediate subtract:
// SUB Xrd, Xrn, #imm12.
func (g *assembler) emitSubImm12(rd, rn, imm12 uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.ASUB
	p.From = obj.Addr{Type: obj.TYPE_CONST, Offset: int64(imm12)}
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitAddImm12 emits immediate add:
// ADD Xrd, Xrn, #imm12.
func (g *assembler) emitAddImm12(rd, rn, imm12 uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AADD
	p.From = obj.Addr{Type: obj.TYPE_CONST, Offset: int64(imm12)}
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitAddReg emits register add:
// ADD Xrd, Xrn, Xrm.
func (g *assembler) emitAddReg(rd, rn, rm uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AADD
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

// emitLoadInputBase loads input pointer shadow from frame:
// LDR Xrd, [SP, #arm64InputBaseOff].
func (g *assembler) emitLoadInputBase(rd uint32) {
	g.emitLdrUnsignedImm64(rd, 31, arm64InputBaseOff)
}

// emitLoadFieldAddr emits field address materialization:
// Xrd = input_base + #offset.
func (g *assembler) emitLoadFieldAddr(rd uint32, offset uint64) {
	g.emitLoadInputBase(regInputScratch)
	g.emitLoadAddrWithOffset(rd, regInputScratch, offset)
}

// emitLoadAddrWithOffset emits address add with fast small-immediate path:
// if offset==0: MOV Xrd, Xbase; if offset<=4095: ADD Xrd, Xbase, #offset; otherwise MOV Xrd, #offset; ADD Xrd, Xbase, Xrd.
func (g *assembler) emitLoadAddrWithOffset(rd, base uint32, offset uint64) {
	if offset == 0 {
		g.emitMovReg(rd, base)
		return
	}
	if offset <= 4095 {
		g.emitAddImm12(rd, base, uint32(offset))
		return
	}
	g.emitLoadImm(rd, offset)
	g.emitAddReg(rd, base, rd)
}

// emitLdrbUnsignedImm32 emits byte load:
// LDRB Wrt, [Xrn, #imm12].
func (g *assembler) emitLdrbUnsignedImm32(rt, rn, imm12 uint32) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AMOVBU
	p.From = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(imm12),
	}
	p.To = regAddr(asmIntReg(rt))
	g.registerProg(p)
}

// emitLoadImm emits constant materialization:
// MOVD Xrd, $imm (assembler expands as needed).
func (g *assembler) emitLoadImm(rd uint32, imm uint64) {
	p := g.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = obj.Addr{Type: obj.TYPE_CONST, Offset: int64(imm)}
	p.To = regAddr(asmIntReg(rd))
	g.registerProg(p)
}

func asmIntReg(reg uint32) int16 {
	switch reg {
	case 0:
		return asmarm64.REG_R0
	case 1:
		return asmarm64.REG_R1
	case 2:
		return asmarm64.REG_R2
	case 3:
		return asmarm64.REG_R3
	case 4:
		return asmarm64.REG_R4
	case 5:
		return asmarm64.REG_R5
	case 6:
		return asmarm64.REG_R6
	case 7:
		return asmarm64.REG_R7
	case 8:
		return asmarm64.REG_R8
	case 9:
		return asmarm64.REG_R9
	case 10:
		return asmarm64.REG_R10
	case 11:
		return asmarm64.REG_R11
	case 12:
		return asmarm64.REG_R12
	case 13:
		return asmarm64.REG_R13
	case 14:
		return asmarm64.REG_R14
	case 15:
		return asmarm64.REG_R15
	case 16:
		return asmarm64.REG_R16
	case 17:
		return asmarm64.REG_R17
	case 18:
		return asmarm64.REG_R18
	case 19:
		return asmarm64.REG_R19
	case 20:
		return asmarm64.REG_R20
	case 21:
		return asmarm64.REG_R21
	case 22:
		return asmarm64.REG_R22
	case 23:
		return asmarm64.REG_R23
	case 24:
		return asmarm64.REG_R24
	case 25:
		return asmarm64.REG_R25
	case 26:
		return asmarm64.REG_R26
	case 27:
		return asmarm64.REG_R27
	case 28:
		return asmarm64.REG_R28
	case 29:
		return asmarm64.REG_R29
	case 30:
		return asmarm64.REG_R30
	case 31:
		return asmarm64.REGSP
	default:
		return asmarm64.REG_R0
	}
}

func asmFloatReg(reg uint32) int16 {
	switch reg {
	case 0:
		return asmarm64.REG_F0
	case 1:
		return asmarm64.REG_F1
	case 2:
		return asmarm64.REG_F2
	case 3:
		return asmarm64.REG_F3
	case 4:
		return asmarm64.REG_F4
	case 5:
		return asmarm64.REG_F5
	case 6:
		return asmarm64.REG_F6
	case 7:
		return asmarm64.REG_F7
	case 8:
		return asmarm64.REG_F8
	case 9:
		return asmarm64.REG_F9
	case 10:
		return asmarm64.REG_F10
	case 11:
		return asmarm64.REG_F11
	case 12:
		return asmarm64.REG_F12
	case 13:
		return asmarm64.REG_F13
	case 14:
		return asmarm64.REG_F14
	case 15:
		return asmarm64.REG_F15
	case 16:
		return asmarm64.REG_F16
	case 17:
		return asmarm64.REG_F17
	case 18:
		return asmarm64.REG_F18
	case 19:
		return asmarm64.REG_F19
	case 20:
		return asmarm64.REG_F20
	case 21:
		return asmarm64.REG_F21
	case 22:
		return asmarm64.REG_F22
	case 23:
		return asmarm64.REG_F23
	case 24:
		return asmarm64.REG_F24
	case 25:
		return asmarm64.REG_F25
	case 26:
		return asmarm64.REG_F26
	case 27:
		return asmarm64.REG_F27
	case 28:
		return asmarm64.REG_F28
	case 29:
		return asmarm64.REG_F29
	case 30:
		return asmarm64.REG_F30
	case 31:
		return asmarm64.REG_F31
	default:
		return asmarm64.REG_F0
	}
}
