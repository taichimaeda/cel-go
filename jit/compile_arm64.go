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

// Frame layout after prologue:
//
//	high address
//	+-----------------------------------------------------------+
//	| caller-save area [SP+callerSaveOff ...)                   |
//	+-----------------------------------------------------------+
//	| regalloc spill area [SP+spillOffset ...)          |
//	+-----------------------------------------------------------+
//	| struct base ptr  [SP+arm64StructBaseOff]                  |
//	+-----------------------------------------------------------+
//	| mirrored LR      [SP+arm64SaveX30FpOff]                   |
//	+-----------------------------------------------------------+
//	| saved FP (X29)   [SP+arm64SaveX29Off]                     |
//	+-----------------------------------------------------------+
//	| saved X25..X19   [SP+arm64SaveX25Off ... arm64SaveX19Off] |
//	+-----------------------------------------------------------+
//	| helper call area                                          |
//	|                  [SP+8 ... SP+8+arm64CallAreaBytes)       |
//	+-----------------------------------------------------------+
//	| saved LR (X30)   [SP+arm64SaveX30Off = SP+0]              |
//	+-----------------------------------------------------------+  <- SP
//	low address

const (
	regArg    = 0
	regRet    = 0
	regTmpInt = 17 // Dedicated scratch for codegen (not allocated).
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
	// Keep a stable copy of the struct pointer in-frame since arm64 internal ABI
	// treats R19-R25 as scratch across calls.
	arm64StructBaseOff = arm64SaveX30FpOff + 8
	// Byte offset where the regalloc spill area begins (16-byte aligned).
	spillOffset = arm64SaveX30FpOff + 16
	// Temporary area in the caller spill region used for parallel argument moves.
	arm64ArgMoveSpillOff = 8
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

// emitPrologue emits frame setup and callee-saved spills:
// SUB SP, SP, #frame; STR X19..X30, [SP+off].
func (as *assembler) emitPrologue() {
	// Preserve LR plus callee-saved registers X19..X25.
	// Keep low frame offsets available for outgoing arg spills.
	as.emitSubImm12(31, 31, uint32(as.frameSize))
	as.emitStoreMemInt(30, 31, arm64SaveX30Off)
	as.emitStoreMemInt(19, 31, arm64SaveX19Off)
	as.emitStoreMemInt(20, 31, arm64SaveX20Off)
	as.emitStoreMemInt(21, 31, arm64SaveX21Off)
	as.emitStoreMemInt(22, 31, arm64SaveX22Off)
	as.emitStoreMemInt(23, 31, arm64SaveX23Off)
	as.emitStoreMemInt(24, 31, arm64SaveX24Off)
	as.emitStoreMemInt(25, 31, arm64SaveX25Off)
	as.emitStoreMemInt(29, 31, arm64SaveX29Off)
	as.emitStoreMemInt(30, 31, arm64SaveX30FpOff)
	// Keep a canonical frame record for Go unwinding:
	// [FP+0] = previous FP, [FP+8] = LR.
	as.emitAddImm12(29, 31, arm64SaveX29Off)
	as.emitStoreMemInt(regArg, 31, arm64StructBaseOff)
}

// emitEpilogue emits callee-saved restores and return:
// LDR X19..X30, [SP+off]; ADD SP, SP, #frame; RET.
func (as *assembler) emitEpilogue() {
	// Restore callee-saved state and return.
	as.emitLoadMemInt(29, 31, arm64SaveX29Off)
	as.emitLoadMemInt(25, 31, arm64SaveX25Off)
	as.emitLoadMemInt(24, 31, arm64SaveX24Off)
	as.emitLoadMemInt(23, 31, arm64SaveX23Off)
	as.emitLoadMemInt(22, 31, arm64SaveX22Off)
	as.emitLoadMemInt(21, 31, arm64SaveX21Off)
	as.emitLoadMemInt(20, 31, arm64SaveX20Off)
	as.emitLoadMemInt(19, 31, arm64SaveX19Off)
	as.emitLoadMemInt(30, 31, arm64SaveX30Off)
	as.emitAddImm12(31, 31, uint32(as.frameSize))
	as.emitRet()
}

// emitInstr lowers one IR instruction to one or more ARM64 instructions.
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
		offset := uint64(ins.Imm)
		switch ins.Type {
		case T_INT64, T_UINT64:
			rd, err := as.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			as.emitLoadMemInt(rd, regTmpInt, 0)
			return nil
		case T_BOOL:
			rd, err := as.intRegOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			as.emitLoadBool(rd, regTmpInt, 0)
			return nil
		case T_FLOAT64:
			fd, err := as.floatRegOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			as.emitLoadMemInt(regTmpInt, regTmpInt, 0)
			as.emitMoveIntToFloat(fd, regTmpInt)
			return nil
		case T_STRING:
			rdPtr, rdLen, err := as.stringRegsOf(ins.Dst)
			if err != nil {
				return err
			}
			as.emitLoadStructFieldAddr(regTmpInt, offset)
			p := as.asmCtxt.NewProg()
			p.As = asmarm64.ALDP
			p.From = obj.Addr{
				Type: obj.TYPE_MEM,
				Reg:  asmIntReg(regTmpInt),
			}
			p.To = obj.Addr{
				Type:   obj.TYPE_REGREG,
				Reg:    asmIntReg(rdPtr),
				Offset: int64(asmIntReg(rdLen)),
			}
			as.registerProg(p)
			return nil
		default:
			return fmt.Errorf("%w: LOAD_FIELD type %v is unsupported on arm64", ErrCodegenUnsupported, ins.Type)
		}
	case LOAD_FIELD_SLICE, LOAD_FIELD_ARRAY:
		if ins.Imm < 0 {
			return fmt.Errorf("%w: %v offset must be non-negative, got %d", ErrCodegenUnsupported, ins.Op, ins.Imm)
		}
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		as.emitLoadStructFieldAddr(rd, uint64(ins.Imm))
		return nil
	case ADD_INT, ADD_UINT:
		return as.emitBinInt(ins, asmarm64.AADD)
	case SUB_INT, SUB_UINT:
		return as.emitBinInt(ins, asmarm64.ASUB)
	case MUL_INT, MUL_UINT:
		return as.emitBinInt(ins, asmarm64.AMUL)
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
		return as.emitBinFloat(ins, asmarm64.AFADDD)
	case SUB_FLOAT:
		return as.emitBinFloat(ins, asmarm64.AFSUBD)
	case MUL_FLOAT:
		return as.emitBinFloat(ins, asmarm64.AFMULD)
	case DIV_FLOAT:
		return as.emitBinFloat(ins, asmarm64.AFDIVD)
	case NEG_FLOAT:
		return as.emitNegFloat(ins)
	case EQ_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmarm64.COND_EQ)
	case NE_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmarm64.COND_NE)
	case GT_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmarm64.COND_GT)
	case GE_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmarm64.COND_GE)
	case LT_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmarm64.COND_LT)
	case LE_FLOAT:
		return as.emitCompareSetBoolFloat(ins, asmarm64.COND_LE)
	case EQ_INT, EQ_UINT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_EQ)
	case NE_INT, NE_UINT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_NE)
	case GT_INT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_GT)
	case GE_INT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_GE)
	case LT_INT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_LT)
	case LE_INT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_LE)
	case LT_UINT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_LO)
	case LE_UINT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_LS)
	case GT_UINT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_HI)
	case GE_UINT:
		return as.emitCompareSetBoolInt(ins, asmarm64.COND_HS)
	case MOVE:
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
	case NOT:
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		rs, err := as.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		p := as.asmCtxt.NewProg()
		p.As = asmarm64.ACMP
		p.From = regAddr(asmarm64.REGZERO)
		p.Reg = asmIntReg(rs)
		as.registerProg(p)
		as.emitSetBoolByte(rd, asmarm64.COND_EQ)
		return nil
	case BR:
		return as.emitBranch(ins, asmarm64.AB, 0)
	case BR_TRUE:
		return as.emitBranch(ins, asmarm64.ACBNZ, ins.Src1)
	case BR_FALSE:
		return as.emitBranch(ins, asmarm64.ACBZ, ins.Src1)
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
	case SPILL_LOAD:
		return as.emitSpillLoad(ins)
	case SPILL_STORE:
		return as.emitSpillStore(ins)
	default:
		return fmt.Errorf("%w: opcode %v is unsupported on arm64", ErrCodegenUnsupported, ins.Op)
	}
}

// emitSpillLoad loads a value from the spill slot at byte offset ins.Imm into ins.Dst.
func (as *assembler) emitSpillLoad(ins Instr) error {
	off := uint32(ins.Imm)
	switch ins.Type {
	case T_FLOAT64:
		fd, err := as.floatRegOf(ins.Dst)
		if err != nil {
			return err
		}
		as.emitLoadMemFloat(fd, 31, off)
	case T_STRING:
		rPtr, rLen, err := as.stringRegsOf(ins.Dst)
		if err != nil {
			return err
		}
		as.emitLoadMemInt(rPtr, 31, off)
		as.emitLoadMemInt(rLen, 31, off+8)
	default:
		rd, err := as.intRegOf(ins.Dst)
		if err != nil {
			return err
		}
		as.emitLoadMemInt(rd, 31, off)
	}
	return nil
}

// emitSpillStore stores a value from ins.Src1 into the spill slot at byte offset ins.Imm.
func (as *assembler) emitSpillStore(ins Instr) error {
	off := uint32(ins.Imm)
	switch ins.Type {
	case T_FLOAT64:
		fs, err := as.floatRegOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitStoreMemFloat(fs, 31, off)
	case T_STRING:
		rPtr, rLen, err := as.stringRegsOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitStoreMemInt(rPtr, 31, off)
		as.emitStoreMemInt(rLen, 31, off+8)
	default:
		rs, err := as.intRegOf(ins.Src1)
		if err != nil {
			return err
		}
		as.emitStoreMemInt(rs, 31, off)
	}
	return nil
}

func (as *assembler) emitCallerSave() {
	entries := as.callerSaves[as.current]
	for _, e := range entries {
		off := uint32(as.callerSaveOff + e.Slot*8)
		if e.IsFloat {
			as.emitStoreMemFloat(uint32(e.Reg-100), 31, off)
		} else {
			as.emitStoreMemInt(uint32(e.Reg), 31, off)
			if e.Reg2 >= 0 {
				as.emitStoreMemInt(uint32(e.Reg2), 31, off+8)
			}
		}
	}
}

func (as *assembler) emitCallerRestore() {
	entries := as.callerSaves[as.current]
	for _, e := range entries {
		off := uint32(as.callerSaveOff + e.Slot*8)
		if e.IsFloat {
			as.emitLoadMemFloat(uint32(e.Reg-100), 31, off)
		} else {
			as.emitLoadMemInt(uint32(e.Reg), 31, off)
			if e.Reg2 >= 0 {
				as.emitLoadMemInt(uint32(e.Reg2), 31, off+8)
			}
		}
	}
}

func (as *assembler) emitCallBuiltin(ins Instr) error {
	as.emitCallerSave()
	fn, ok := builtinFunction(ins.BuiltinID)
	if !ok {
		return fmt.Errorf("%w: builtin %v has no function value", ErrCodegenUnsupported, ins.BuiltinID)
	}
	var err error
	switch ins.BuiltinID {
	case BuiltinStrSize:
		err = as.emitCallStringToInt(ins, fn)
	case BuiltinStrEq, BuiltinStrNe, BuiltinStrContains, BuiltinStrStarts, BuiltinStrEnds:
		err = as.emitCallTwoStringsToBool(ins, fn)
	case BuiltinStrConcat:
		err = as.emitCallTwoStringsToString(ins, fn)
	case BuiltinListContainsStringSlice:
		err = as.emitCallSliceToBool(ins, fn)
	case BuiltinListContainsStringArray:
		err = as.emitCallArrayToBool(ins, fn)
	default:
		return fmt.Errorf("%w: builtin %v is unsupported on arm64", ErrCodegenUnsupported, ins.BuiltinID)
	}
	if err != nil {
		return err
	}
	as.emitCallerRestore()
	return nil
}

// emitCallStringToInt emits string -> int64 helper call:
// MOV args (X0,X1), BLR fn, result in X0.
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
		as.emitMoveIntToInt(dst, 0)
	}
	return nil
}

// emitCallTwoStringsToBool emits string,string -> bool helper call:
// MOV args (X0..X3), BLR fn, result in X0.
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
	if dst != 0 {
		as.emitMoveIntToInt(dst, 0)
	}
	return nil
}

// emitCallTwoStringsToString emits string,string -> string helper call:
// MOV args (X0..X3), BLR fn, result in (X0,X1).
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
		as.emitMoveIntToInt(dstPtr, 0)
	}
	if dstLen != 1 {
		as.emitMoveIntToInt(dstLen, 1)
	}
	return nil
}

// emitCallSliceToBool emits (string, *sliceHeader) -> bool helper call:
// X0/X1=needle ptr/len, X2=list field address, BLR fn, result in X0.
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
		{0, needlePtr},
		{1, needleLen},
		{2, listAddr},
	})
	if err := as.emitCallFunction(fn); err != nil {
		return err
	}
	if dst != 0 {
		as.emitMoveIntToInt(dst, 0)
	}
	return nil
}

// emitCallArrayToBool emits (string, *array, len) -> bool helper call:
// X0/X1=needle ptr/len, X2=array address, X3=len, BLR fn, result in X0.
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
		{0, needlePtr},
		{1, needleLen},
		{2, arrayAddr},
		{3, regTmpInt},
	})
	if err := as.emitCallFunction(fn); err != nil {
		return err
	}
	if dst != 0 {
		as.emitMoveIntToInt(dst, 0)
	}
	return nil
}

// emitMoveStringToArgs moves one string pair to ABI argument registers:
// X{argBase}=ptr, X{argBase+1}=len.
func (as *assembler) emitMoveStringToArgs(argBase, ptrReg, lenReg uint32) {
	as.emitParallelIntMoves([][2]uint32{
		{argBase, ptrReg},
		{argBase + 1, lenReg},
	})
}

// emitMoveTwoStringsToArgs moves two string pairs to ABI argument registers:
// X0/X1=lhs ptr/len, X2/X3=rhs ptr/len.
func (as *assembler) emitMoveTwoStringsToArgs(aPtr, aLen, bPtr, bLen uint32) {
	as.emitParallelIntMoves([][2]uint32{
		{0, aPtr},
		{1, aLen},
		{2, bPtr},
		{3, bLen},
	})
}

// emitParallelIntMoves performs parallel register moves through stack spills:
// STR src_i, [SP+off_i] then LDR dst_i, [SP+off_i].
func (as *assembler) emitParallelIntMoves(moves [][2]uint32) {
	if len(moves) == 0 {
		return
	}
	count := 0
	for _, m := range moves {
		if m[0] == m[1] {
			continue
		}
		off := uint32(arm64ArgMoveSpillOff + count*8)
		as.emitStoreMemInt(m[1], 31, off)
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
		as.emitLoadMemInt(m[0], 31, off)
		count++
	}
}

// emitCallFunction emits a function-value indirect call sequence:
// MOVD $fn, X17; MOVD X17, X26; MOVD (X17), X17; BLR X17.
func (as *assembler) emitCallFunction(fn uintptr) error {
	if fn == 0 {
		return fmt.Errorf("%w: helper function pointer is nil", ErrCodegenUnsupported)
	}
	as.emitLoadImm(regTmpInt, uint64(fn))
	as.emitMoveIntToInt(26, regTmpInt)
	as.emitLoadMemInt(regTmpInt, regTmpInt, 0)
	as.emitBlr(regTmpInt)
	return nil
}

// emitBinInt emits a 2-operand integer ALU op in 3-register form:
// OP Xdst, Xsrc1, Xsrc2.
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
	p := as.asmCtxt.NewProg()
	p.As = op
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
	return nil
}

// emitDivInt emits signed/unsigned integer divide:
// SDIV/UDIV Xdst, Xsrc1, Xsrc2.
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
	p := as.asmCtxt.NewProg()
	if signed {
		p.As = asmarm64.ASDIV
	} else {
		p.As = asmarm64.AUDIV
	}
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
	return nil
}

// emitModInt emits integer remainder via:
// Xquotient = SDIV/UDIV Xrn,Xrm; Xquotient = MUL Xquotient,Xrm; Xrd = SUB Xrn,Xquotient.
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
	quotient := uint32(regTmpInt)
	div := as.asmCtxt.NewProg()
	if signed {
		div.As = asmarm64.ASDIV
	} else {
		div.As = asmarm64.AUDIV
	}
	div.From = regAddr(asmIntReg(rm))
	div.Reg = asmIntReg(rn)
	div.To = regAddr(asmIntReg(quotient))
	as.registerProg(div)
	// quotient = quotient * rm
	mul := as.asmCtxt.NewProg()
	mul.As = asmarm64.AMUL
	mul.From = regAddr(asmIntReg(rm))
	mul.Reg = asmIntReg(quotient)
	mul.To = regAddr(asmIntReg(quotient))
	as.registerProg(mul)
	// rd = rn - quotient
	sub := as.asmCtxt.NewProg()
	sub.As = asmarm64.ASUB
	sub.From = regAddr(asmIntReg(quotient))
	sub.Reg = asmIntReg(rn)
	sub.To = regAddr(asmIntReg(rd))
	as.registerProg(sub)
	return nil
}

// emitNegInt emits integer negate:
// NEG Xdst, Xsrc1.
func (as *assembler) emitNegInt(ins Instr) error {
	rd, err := as.intRegOf(ins.Dst)
	if err != nil {
		return err
	}
	rs, err := as.intRegOf(ins.Src1)
	if err != nil {
		return err
	}
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.ANEG
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
	return nil
}

// emitBinFloat emits 2-operand float ALU op:
// FOP Ddst, Dsrc1, Dsrc2.
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
	p := as.asmCtxt.NewProg()
	p.As = op
	p.From = regAddr(asmFloatReg(fm))
	p.Reg = asmFloatReg(fn)
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
	return nil
}

// emitNegFloat emits float negate:
// FNEG Ddst, Dsrc1.
func (as *assembler) emitNegFloat(ins Instr) error {
	fd, err := as.floatRegOf(ins.Dst)
	if err != nil {
		return err
	}
	fn, err := as.floatRegOf(ins.Src1)
	if err != nil {
		return err
	}
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AFNEGD
	p.From = regAddr(asmFloatReg(fn))
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
	return nil
}

// emitCompareSetBoolInt emits integer compare+set:
// CMP Xsrc1, Xsrc2; CSET Xdst, cond.
func (as *assembler) emitCompareSetBoolInt(ins Instr, cond int16) error {
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
	as.emitSetBoolByte(rd, cond)
	return nil
}

// emitCompareSetBoolFloat emits float compare+set:
// FCMP Dsrc1, Dsrc2; CSET Xdst, cond.
func (as *assembler) emitCompareSetBoolFloat(ins Instr, cond int16) error {
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
	as.emitSetBoolByte(rd, cond)
	return nil
}

// emitSetBoolByte emits CSET into destination register:
// CSET Xrd, cond.
func (as *assembler) emitSetBoolByte(rd uint32, cond int16) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.ACSET
	p.From = regAddr(cond)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitCmpRegInt emits integer compare:
// CMP Xrn, Xrm.
func (as *assembler) emitCmpRegInt(rn, rm uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.ACMP
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	as.registerProg(p)
}

// emitCmpRegFloat emits float compare:
// FCMP Dfn, Dfm.
func (as *assembler) emitCmpRegFloat(fn, fm uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AFCMPD
	p.From = regAddr(asmFloatReg(fm))
	p.Reg = asmFloatReg(fn)
	as.registerProg(p)
}

// emitBranch emits B/CBZ/CBNZ with an unresolved TYPE_BRANCH target.
func (as *assembler) emitBranch(ins Instr, op obj.As, src VReg) error {
	p := as.asmCtxt.NewProg()
	p.As = op
	if src != 0 {
		rs, err := as.intRegOf(src)
		if err != nil {
			return err
		}
		p.From = regAddr(asmIntReg(rs))
	}
	p.To.Type = obj.TYPE_BRANCH
	as.registerProg(p)
	as.registerBranch(int(ins.Lbl), p)
	return nil
}

// emitMoveIntToInt emits integer register move:
// MOVD Xrd, Xrs.
func (as *assembler) emitMoveIntToInt(rd, rs uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitMoveIntToFloat emits integer->float register bit-move:
// FMOV Dfd, Xrs.
func (as *assembler) emitMoveIntToFloat(fd, rs uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AFMOVD
	p.From = regAddr(asmIntReg(rs))
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
}

// emitMoveFloatToFloat emits float register move:
// FMOV Dfd, Dfs.
func (as *assembler) emitMoveFloatToFloat(fd, fs uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AFMOVD
	p.From = regAddr(asmFloatReg(fs))
	p.To = regAddr(asmFloatReg(fd))
	as.registerProg(p)
}

// emitLoadImm emits constant materialization:
// MOVD Xrd, $imm (assembler expands as needed).
func (as *assembler) emitLoadImm(rd uint32, imm uint64) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = constAddr(int64(imm))
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitLoadMemInt emits 64-bit load:
// LDR Xrt, [Xrn, #byteOffset].
func (as *assembler) emitLoadMemInt(rt, rn, byteOffset uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(byteOffset),
	}
	p.To = regAddr(asmIntReg(rt))
	as.registerProg(p)
}

// emitStoreMemInt emits 64-bit store:
// STR Xrt, [Xrn, #byteOffset].
func (as *assembler) emitStoreMemInt(rt, rn, byteOffset uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AMOVD
	p.From = regAddr(asmIntReg(rt))
	p.To = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(byteOffset),
	}
	as.registerProg(p)
}

// emitLoadMemFloat emits float64 load:
// FMOVD Dft, [Xrn, #byteOffset].
func (as *assembler) emitLoadMemFloat(ft, rn, byteOffset uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AFMOVD
	p.From = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(byteOffset),
	}
	p.To = regAddr(asmFloatReg(ft))
	as.registerProg(p)
}

// emitStoreMemFloat emits float64 store:
// FMOVD [Xrn, #byteOffset], Dfs.
func (as *assembler) emitStoreMemFloat(fs, rn, byteOffset uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AFMOVD
	p.From = regAddr(asmFloatReg(fs))
	p.To = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(byteOffset),
	}
	as.registerProg(p)
}

// emitSubImm12 emits immediate subtract:
// SUB Xrd, Xrn, #imm12.
func (as *assembler) emitSubImm12(rd, rn, imm12 uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.ASUB
	p.From = constAddr(int64(imm12))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitAddImm12 emits immediate add:
// ADD Xrd, Xrn, #imm12.
func (as *assembler) emitAddImm12(rd, rn, imm12 uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AADD
	p.From = constAddr(int64(imm12))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitAddReg emits register add:
// ADD Xrd, Xrn, Xrm.
func (as *assembler) emitAddReg(rd, rn, rm uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AADD
	p.From = regAddr(asmIntReg(rm))
	p.Reg = asmIntReg(rn)
	p.To = regAddr(asmIntReg(rd))
	as.registerProg(p)
}

// emitLoadBool emits byte load:
// LDRB Wrt, [Xrn, #imm12].
func (as *assembler) emitLoadBool(rt, rn, imm12 uint32) {
	p := as.asmCtxt.NewProg()
	p.As = asmarm64.AMOVBU
	p.From = obj.Addr{
		Type:   obj.TYPE_MEM,
		Reg:    asmIntReg(rn),
		Offset: int64(imm12),
	}
	p.To = regAddr(asmIntReg(rt))
	as.registerProg(p)
}

// emitBlr emits indirect call:
// BLR Xrn.
func (as *assembler) emitBlr(rn uint32) {
	p := as.asmCtxt.NewProg()
	p.As = obj.ACALL
	p.To = regAddr(asmIntReg(rn))
	as.registerProg(p)
}

// emitRet emits return instruction:
// RET X30.
func (as *assembler) emitRet() {
	p := as.asmCtxt.NewProg()
	p.As = obj.ARET
	p.To = regAddr(asmarm64.REG_R30)
	as.registerProg(p)
}

// emitLoadStructBase loads struct pointer shadow from frame:
// LDR Xrd, [SP, #arm64StructBaseOff].
func (as *assembler) emitLoadStructBase(rd uint32) {
	as.emitLoadMemInt(rd, 31, arm64StructBaseOff)
}

// emitLoadStructFieldAddr emits field address materialization:
// Xrd = struct_base + #offset.
func (as *assembler) emitLoadStructFieldAddr(rd uint32, offset uint64) {
	as.emitLoadStructBase(rd)
	if offset == 0 {
		return
	}
	if offset <= 4095 {
		as.emitAddImm12(rd, rd, uint32(offset))
		return
	}
	as.emitLoadImm(regTmpInt, offset)
	as.emitAddReg(rd, rd, regTmpInt)
}

func frameBytes(numSpillSlots, numSaveSlots int) int {
	n := spillOffset + numSpillSlots*8 + numSaveSlots*8
	if n < spillOffset {
		n = spillOffset
	}
	// 16-byte align.
	if n%16 != 0 {
		n += 16 - n%16
	}
	return n
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
