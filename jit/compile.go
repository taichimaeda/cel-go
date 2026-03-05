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

	"github.com/bytedance/sonic/loader"
	celast "github.com/google/cel-go/common/ast"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/twitchyliquid64/golang-asm/asm/arch"
	"github.com/twitchyliquid64/golang-asm/obj"
)

// NativeProgram contains a compiled native evaluator entrypoint and its expected input type.
type NativeProgram struct {
	ActivationType reflect.Type
	EvalFunc       NativeEvalFunc
}

// NativeEvalFunc evaluates a compiled program against a struct input pointer.
type NativeEvalFunc func(input unsafe.Pointer) bool

type nativeEvalFunc func(input unsafe.Pointer) uint64

type assembler struct {
	currentIR  int
	progs      []*obj.Prog
	progMap    map[int]*obj.Prog
	branchMap  map[int][]*obj.Prog
	vregLocs   map[VReg]Location
	stringPool []string
	asmCtxt    *obj.Link
	asmArch    *arch.Arch
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
	regMap := Allocate(prog)
	if regMap == nil {
		return nil, fmt.Errorf("%w: register allocation returned nil", ErrCodegenUnsupported)
	}
	stringPool := append([]string(nil), prog.StringPool...)
	bytes, err := assembleNative(prog, regMap, stringPool)
	if err != nil {
		return nil, err
	}
	fn, loadedFn, err := loadNative(bytes)
	if err != nil {
		return nil, err
	}
	return func(input unsafe.Pointer) bool {
		out := fn(input) != 0
		runtime.KeepAlive(loadedFn)
		runtime.KeepAlive(stringPool)
		return out
	}, nil
}

func assembleNative(
	prog *Program,
	regMap map[VReg]Location,
	stringPool []string,
) ([]byte, error) {
	asmCtxt, asmArch := newNativeAsmContext()
	if asmCtxt == nil || asmArch == nil {
		return nil, fmt.Errorf("%w: assembler context is unavailable for GOARCH=%s", ErrCodegenUnsupported, runtime.GOARCH)
	}
	g := &assembler{
		branchMap:  make(map[int][]*obj.Prog, 16),
		progs:      make([]*obj.Prog, 0, len(prog.Instrs)*6+16),
		progMap:    make(map[int]*obj.Prog, len(prog.Instrs)),
		vregLocs:   regMap,
		stringPool: stringPool,
		asmCtxt:    asmCtxt,
		asmArch:    asmArch,
	}
	g.currentIR = -1
	g.emitPrologue()
	for i, ins := range prog.Instrs {
		g.currentIR = i
		if err := g.emitInstr(ins); err != nil {
			return nil, err
		}
	}
	g.currentIR = -1
	g.emitEpilogue()
	if err := g.resolveBranches(len(prog.Instrs)); err != nil {
		return nil, err
	}
	return g.bytes()
}

var nativeLoadSequence atomic.Uint64

func loadNative(bytes []byte) (nativeEvalFunc, loader.Function, error) {
	seq := nativeLoadSequence.Add(1)
	moduleName := fmt.Sprintf("cel.jit.%s.%d.", runtime.GOARCH, seq)
	funcName := fmt.Sprintf("nativeEval%d", seq)
	loadedFn := loader.Loader{
		Name: moduleName,
		File: fmt.Sprintf("jit/native_%s.s", runtime.GOARCH),
		Options: loader.Options{
			NoPreempt: true,
		},
	}.LoadOne(
		bytes,
		funcName,
		nativeFrameBytes,
		8,
		[]bool{true},
		[]bool{},
		loader.Pcdata{
			{PC: uint32(len(bytes)), Val: nativeFrameBytes},
		},
	)
	raw := unsafe.Pointer(loadedFn)
	fn := *(*nativeEvalFunc)(unsafe.Pointer(&raw))
	return fn, loadedFn, nil
}

func (g *assembler) registerProg(p *obj.Prog) {
	p.Ctxt = g.asmCtxt
	if n := len(g.progs); n > 0 {
		g.progs[n-1].Link = p
	}
	g.progs = append(g.progs, p)
	if g.currentIR >= 0 {
		if _, found := g.progMap[g.currentIR]; !found {
			g.progMap[g.currentIR] = p
		}
	}
}

func (g *assembler) registerBranch(targetIR int, branch *obj.Prog) {
	g.branchMap[targetIR] = append(g.branchMap[targetIR], branch)
}

func (g *assembler) resolveBranches(numInstrs int) error {
	targets := make([]*obj.Prog, numInstrs+1)
	for ir, p := range g.progMap {
		if ir < 0 || ir > numInstrs {
			continue
		}
		targets[ir] = p
	}
	for i := numInstrs - 1; i >= 0; i-- {
		if targets[i] == nil {
			targets[i] = targets[i+1]
		}
	}
	for targetIR, refs := range g.branchMap {
		if targetIR < 0 || targetIR > numInstrs {
			return fmt.Errorf("%w: branch target IR index %d out of range [0,%d]", ErrCodegenUnsupported, targetIR, numInstrs)
		}
		target := targets[targetIR]
		if target == nil {
			return fmt.Errorf("%w: branch target IR index %d resolved to nil", ErrCodegenUnsupported, targetIR)
		}
		for _, branch := range refs {
			if branch == nil {
				return fmt.Errorf("%w: nil branch reference for target IR index %d", ErrCodegenUnsupported, targetIR)
			}
			branch.To.SetTarget(target)
		}
	}
	return nil
}

func (g *assembler) bytes() ([]byte, error) {
	if len(g.progs) == 0 {
		return nil, fmt.Errorf("%w: no assembled instructions were emitted", ErrCodegenUnsupported)
	}
	sym := &obj.LSym{
		Name: fmt.Sprintf("cel.jit.%s.asm.%p", runtime.GOARCH, g),
		Func: &obj.FuncInfo{},
	}
	text := g.asmCtxt.NewProg()
	text.Ctxt = g.asmCtxt
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
	text.Link = g.progs[0]

	errCount := g.asmCtxt.Errors
	g.asmArch.Assemble(g.asmCtxt, sym, g.asmCtxt.NewProg)
	if g.asmCtxt.Errors != errCount {
		return nil, fmt.Errorf("%w: assembler reported errors", ErrCodegenUnsupported)
	}
	if len(sym.P) == 0 {
		return nil, fmt.Errorf("%w: assembled machine code is empty", ErrCodegenUnsupported)
	}
	return append([]byte(nil), sym.P...), nil
}

func (g *assembler) locOf(v VReg) (Location, error) {
	if v == 0 {
		return Location{}, fmt.Errorf("%w: virtual register 0 is invalid", ErrCodegenUnsupported)
	}
	loc, found := g.vregLocs[v]
	if !found {
		return Location{}, fmt.Errorf("%w: virtual register v%d has no assigned location", ErrCodegenUnsupported, v)
	}
	if loc.IsSpill {
		return Location{}, fmt.Errorf("%w: virtual register v%d was spilled to slot %d", ErrCodegenUnsupported, v, loc.Slot)
	}
	return loc, nil
}

func (g *assembler) intRegOf(v VReg) (uint32, error) {
	loc, err := g.locOf(v)
	if err != nil {
		return 0, err
	}
	if loc.Reg < 0 || loc.Reg >= 100 {
		return 0, fmt.Errorf("%w: virtual register v%d has non-integer location reg=%d", ErrCodegenUnsupported, v, loc.Reg)
	}
	return uint32(loc.Reg), nil
}

func (g *assembler) floatRegOf(v VReg) (uint32, error) {
	loc, err := g.locOf(v)
	if err != nil {
		return 0, err
	}
	if loc.Reg < 100 || loc.Reg2 >= 0 {
		return 0, fmt.Errorf("%w: virtual register v%d has non-float location reg=%d reg2=%d", ErrCodegenUnsupported, v, loc.Reg, loc.Reg2)
	}
	return uint32(loc.Reg - 100), nil
}

func (g *assembler) stringRegsOf(v VReg) (uint32, uint32, error) {
	loc, err := g.locOf(v)
	if err != nil {
		return 0, 0, err
	}
	if loc.Reg < 0 || loc.Reg2 < 0 || loc.Reg >= 100 || loc.Reg2 >= 100 {
		return 0, 0, fmt.Errorf("%w: virtual register v%d is not assigned as a string pair (reg=%d reg2=%d)", ErrCodegenUnsupported, v, loc.Reg, loc.Reg2)
	}
	return uint32(loc.Reg), uint32(loc.Reg2), nil
}

func regAddr(reg int16) obj.Addr {
	return obj.Addr{Type: obj.TYPE_REG, Reg: reg}
}

func memAddr(base int16, off int32) obj.Addr {
	return obj.Addr{Type: obj.TYPE_MEM, Reg: base, Offset: int64(off)}
}
