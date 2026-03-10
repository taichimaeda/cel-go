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
	"runtime"
	"testing"

	"github.com/taichimaeda/cel-go/jit"
)

func TestAllocateDoubleWideRegister(t *testing.T) {
	p := &jit.Program{
		Instrs: []jit.Instr{
			{Op: jit.CONST_STRING, Dst: 1, Imm: 0, Type: jit.T_STRING},
			{Op: jit.RETURN, Src1: 1, Type: jit.T_STRING},
		},
	}
	res, _ := jit.Allocate(p)
	loc, found := res[1]
	if !found {
		t.Fatalf("expected allocation for vreg 1")
	}
	if loc.Slot >= 0 {
		t.Fatalf("expected register allocation for vreg 1, got spill: %#v", loc)
	}
	if loc.Reg2 < 0 {
		t.Fatalf("expected second register for string value, got %#v", loc)
	}
}

func TestAllocateSpillOccurs(t *testing.T) {
	const regs = 20
	instrs := make([]jit.Instr, 0, regs*2+1)
	for i := 0; i < regs; i++ {
		v := jit.VReg(i + 1)
		instrs = append(instrs, jit.Instr{
			Op:   jit.CONST_STRING,
			Dst:  v,
			Imm:  int64(i % 8),
			Type: jit.T_STRING,
		})
	}
	for i := 0; i < regs; i++ {
		v := jit.VReg(i + 1)
		instrs = append(instrs, jit.Instr{
			Op:   jit.BR_TRUE,
			Src1: v,
			Lbl:  0,
			Type: jit.T_BOOL,
		})
	}
	instrs = append(instrs, jit.Instr{Op: jit.RETURN, Src1: 1, Type: jit.T_STRING})

	p := &jit.Program{Instrs: instrs}
	res, numSpills := jit.Allocate(p)
	if res == nil {
		t.Fatalf("expected non-nil reg map")
	}
	if numSpills == 0 {
		t.Fatalf("expected spills to occur")
	}
	hasSpill := false
	for _, loc := range res {
		if loc.Slot >= 0 {
			hasSpill = true
			break
		}
	}
	if !hasSpill {
		t.Fatalf("expected at least one spilled vreg in allocation")
	}
}

func TestAllocateARM64UsesExpandedIntRegPool(t *testing.T) {
	p := &jit.Program{
		Instrs: []jit.Instr{
			{Op: jit.CONST_STRING, Dst: 1, Imm: 0, Type: jit.T_STRING},
			{Op: jit.RETURN, Src1: 1, Type: jit.T_STRING},
		},
	}
	if runtime.GOARCH != "arm64" {
		t.Skip("arm64-specific register-pool expectation")
	}
	res, _ := jit.Allocate(p)
	loc, found := res[1]
	if !found {
		t.Fatalf("expected allocation for vreg 1")
	}
	if loc.Slot >= 0 {
		t.Fatalf("expected register allocation for vreg 1, got spill: %#v", loc)
	}
	if loc.Reg < 0 || loc.Reg > 15 {
		t.Fatalf("expected caller-saved register assignment in R0-R15, got %#v", loc)
	}
	if loc.Reg == 19 || loc.Reg2 == 19 {
		t.Fatalf("R19 must stay reserved as input-base, got %#v", loc)
	}
}
