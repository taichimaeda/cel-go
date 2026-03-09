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

// Package jit provides a best-effort native compilation path for CEL programs.
package jit

import (
	"fmt"
	"reflect"
	"runtime"
	"sync/atomic"
	"unsafe"

	celast "github.com/taichimaeda/cel-go/common/ast"
	celtypes "github.com/taichimaeda/cel-go/common/types"
	"github.com/taichimaeda/sonic/loader"
	"github.com/twitchyliquid64/golang-asm/asm/arch"
	"github.com/twitchyliquid64/golang-asm/obj"
)

type NativeProgram struct {
	ActivationType reflect.Type
	EvalFunc       NativeEvalFunc
}

type NativeEvalFunc func(input unsafe.Pointer) bool

type nativeEvalFunc func(input unsafe.Pointer) uint64

type assembler struct {
	current     int                  // Current IR instruction index (-1 for prologue/epilogue).
	progs       []*obj.Prog          // All assembled instructions in order, linked together.
	progMap     map[int]*obj.Prog    // Maps IR instruction index to its first assembled instruction.
	branchMap   map[int][]*obj.Prog  // Maps target IR index to branch instructions that jump to it.
	stringPool  []string             // Interned string literals referenced by CONST_STRING instructions.
	vregLocs    map[VReg]Location    // Maps virtual register to physical register/spill slot.
	callerSaves map[int][]CallerSave // Maps CALL_BUILTIN index to registers to save.
	frameBytes  int                  // Total stack frame size determined by caller saves.

	asmCtxt *obj.Link
	asmArch *arch.Arch
}

// Compile translates, allocates, and prepares a native evaluator.
func Compile(ast *celast.AST, activationType reflect.Type) (*NativeProgram, error) {
	evalFunc, err := compileNative(ast, activationType)
	if err != nil {
		return nil, err
	}
	return &NativeProgram{
		ActivationType: activationType,
		EvalFunc:       evalFunc,
	}, nil
}

func compileNative(
	ast *celast.AST,
	activationType reflect.Type,
) (NativeEvalFunc, error) {
	if ast.GetType(ast.Expr().ID()).Kind() != celtypes.BoolKind {
		return nil, fmt.Errorf("%w: root expression must evaluate to bool", ErrCodegenUnsupported)
	}
	if activationType == nil {
		return nil, fmt.Errorf("%w: activation type is nil", ErrCodegenUnsupported)
	}
	prog, err := Translate(ast, activationType)
	if err != nil {
		return nil, err
	}
	vregLocs := Allocate(prog)
	if vregLocs == nil {
		return nil, fmt.Errorf("%w: register allocation returned nil", ErrCodegenUnsupported)
	}
	stringPool := append([]string(nil), prog.StringPool...)
	_, _, calleeSet := buildRegSets()
	callerSaves, maxSlots := computeCallerSaves(prog.Instrs, vregLocs, calleeSet)
	frameBytes := frameBytes(maxSlots)
	bytes, err := assembleNative(prog, stringPool, vregLocs, callerSaves, frameBytes)
	if err != nil {
		return nil, err
	}
	fn, fnVal, err := loadNative(bytes, frameBytes)
	if err != nil {
		return nil, err
	}
	return func(input unsafe.Pointer) bool {
		out := fn(input) != 0
		runtime.KeepAlive(fnVal)
		runtime.KeepAlive(stringPool)
		return out
	}, nil
}

func assembleNative(
	prog *Program,
	stringPool []string,
	vregLocs map[VReg]Location,
	callerSaves map[int][]CallerSave,
	frameBytes int,
) ([]byte, error) {
	asmCtxt, asmArch := newNativeAsmContext()
	if asmCtxt == nil || asmArch == nil {
		return nil, fmt.Errorf("%w: assembler context is unavailable for GOARCH=%s", ErrCodegenUnsupported, runtime.GOARCH)
	}
	as := &assembler{
		branchMap:   make(map[int][]*obj.Prog, 16),
		progs:       make([]*obj.Prog, 0, len(prog.Instrs)*6+16),
		progMap:     make(map[int]*obj.Prog, len(prog.Instrs)),
		stringPool:  stringPool,
		vregLocs:    vregLocs,
		callerSaves: callerSaves,
		frameBytes:  frameBytes,

		asmArch: asmArch,
		asmCtxt: asmCtxt,
	}
	as.current = -1
	as.emitPrologue()
	for i, ins := range prog.Instrs {
		as.current = i
		if err := as.emitInstr(ins); err != nil {
			return nil, err
		}
	}
	as.current = -1
	as.emitEpilogue()
	if err := as.resolveBranches(len(prog.Instrs)); err != nil {
		return nil, err
	}
	code, err := as.bytes()
	if err != nil {
		return nil, err
	}
	return code, nil
}

var nativeLoadSequence atomic.Uint64

func loadNative(bytes []byte, frameBytes int) (nativeEvalFunc, loader.Function, error) {
	seq := nativeLoadSequence.Add(1)
	moduleName := fmt.Sprintf("cel.jit.%s.%d.", runtime.GOARCH, seq)
	funcName := fmt.Sprintf("nativeEval%d", seq)
	ld := loader.Loader{
		Name: moduleName,
		File: fmt.Sprintf("jit/native_%s.s", runtime.GOARCH),
		Options: loader.Options{
			NoPreempt: true,
		},
	}
	fnVal := ld.LoadOne(
		bytes,
		funcName,
		frameBytes,
		8,
		[]bool{true},
		[]bool{},
		loader.Pcdata{
			{PC: uint32(len(bytes)), Val: int32(frameBytes)},
		},
	)
	fnRaw := unsafe.Pointer(fnVal)
	fn := *(*nativeEvalFunc)(unsafe.Pointer(&fnRaw))
	return fn, fnVal, nil
}

func (as *assembler) registerProg(p *obj.Prog) {
	p.Ctxt = as.asmCtxt
	if n := len(as.progs); n > 0 {
		as.progs[n-1].Link = p
	}
	as.progs = append(as.progs, p)
	if as.current >= 0 {
		// Register only non-prologue/epilogue instructions.
		if _, found := as.progMap[as.current]; !found {
			as.progMap[as.current] = p
		}
	}
}

func (as *assembler) registerBranch(targetIR int, branch *obj.Prog) {
	as.branchMap[targetIR] = append(as.branchMap[targetIR], branch)
}

func (as *assembler) resolveBranches(numInstrs int) error {
	targets := make([]*obj.Prog, numInstrs+1)
	for i, p := range as.progMap {
		targets[i] = p
	}
	for i := numInstrs - 1; i >= 0; i-- {
		if targets[i] == nil {
			targets[i] = targets[i+1]
		}
	}
	for i, refs := range as.branchMap {
		if i < 0 || i > numInstrs {
			return fmt.Errorf("%w: branch target IR index %d out of range [0,%d]", ErrCodegenUnsupported, i, numInstrs)
		}
		target := targets[i]
		if target == nil {
			return fmt.Errorf("%w: branch target IR index %d resolved to nil", ErrCodegenUnsupported, i)
		}
		for _, branch := range refs {
			if branch == nil {
				return fmt.Errorf("%w: nil branch reference for target IR index %d", ErrCodegenUnsupported, i)
			}
			branch.To.SetTarget(target)
		}
	}
	return nil
}

func (as *assembler) bytes() ([]byte, error) {
	if len(as.progs) == 0 {
		return nil, fmt.Errorf("%w: no assembled instructions were emitted", ErrCodegenUnsupported)
	}
	sym := &obj.LSym{
		Name: fmt.Sprintf("cel.jit.%s.asm.%p", runtime.GOARCH, as),
		Func: &obj.FuncInfo{},
	}
	text := as.asmCtxt.NewProg()
	text.Ctxt = as.asmCtxt
	text.As = obj.ATEXT
	text.From = obj.Addr{
		Type: obj.TYPE_MEM,
		Name: obj.NAME_EXTERN,
		Sym:  sym,
	}
	text.To = obj.Addr{
		Type:   obj.TYPE_TEXTSIZE,
		Offset: 0,
		Val:    int32(-1),
	}
	sym.Func.Text = text
	text.Link = as.progs[0]

	errCount := as.asmCtxt.Errors
	as.asmArch.Assemble(as.asmCtxt, sym, as.asmCtxt.NewProg)
	if as.asmCtxt.Errors != errCount {
		return nil, fmt.Errorf("%w: assembler reported errors", ErrCodegenUnsupported)
	}
	if len(sym.P) == 0 {
		return nil, fmt.Errorf("%w: assembled machine code is empty", ErrCodegenUnsupported)
	}
	return append([]byte(nil), sym.P...), nil
}

func (as *assembler) locOf(v VReg) (Location, error) {
	if v == 0 {
		return Location{}, fmt.Errorf("%w: virtual register 0 is invalid", ErrCodegenUnsupported)
	}
	loc, found := as.vregLocs[v]
	if !found {
		return Location{}, fmt.Errorf("%w: virtual register v%d has no assigned location", ErrCodegenUnsupported, v)
	}
	if loc.Slot >= 0 {
		return Location{}, fmt.Errorf("%w: virtual register v%d was spilled to slot %d", ErrCodegenUnsupported, v, loc.Slot)
	}
	return loc, nil
}

func (as *assembler) intRegOf(v VReg) (uint32, error) {
	loc, err := as.locOf(v)
	if err != nil {
		return 0, err
	}
	if loc.Reg < 0 || loc.Reg >= 100 {
		return 0, fmt.Errorf("%w: virtual register v%d has non-integer location reg=%d", ErrCodegenUnsupported, v, loc.Reg)
	}
	return uint32(loc.Reg), nil
}

func (as *assembler) floatRegOf(v VReg) (uint32, error) {
	loc, err := as.locOf(v)
	if err != nil {
		return 0, err
	}
	if loc.Reg < 100 || loc.Reg2 >= 0 {
		return 0, fmt.Errorf("%w: virtual register v%d has non-float location reg=%d reg2=%d", ErrCodegenUnsupported, v, loc.Reg, loc.Reg2)
	}
	return uint32(loc.Reg - 100), nil
}

func (as *assembler) stringRegsOf(v VReg) (uint32, uint32, error) {
	loc, err := as.locOf(v)
	if err != nil {
		return 0, 0, err
	}
	if loc.Reg < 0 || loc.Reg2 < 0 || loc.Reg >= 100 || loc.Reg2 >= 100 {
		return 0, 0, fmt.Errorf("%w: virtual register v%d is not assigned as a string pair (reg=%d reg2=%d)", ErrCodegenUnsupported, v, loc.Reg, loc.Reg2)
	}
	return uint32(loc.Reg), uint32(loc.Reg2), nil
}

func constAddr(val int64) obj.Addr {
	return obj.Addr{Type: obj.TYPE_CONST, Offset: val}
}

func regAddr(reg int16) obj.Addr {
	return obj.Addr{Type: obj.TYPE_REG, Reg: reg}
}

func memAddr(base int16, off int32) obj.Addr {
	return obj.Addr{Type: obj.TYPE_MEM, Reg: base, Offset: int64(off)}
}
