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

import "testing"

func testProg(numVRegs int, instrs ...Instr) *Program {
	return &Program{Instrs: instrs, NumVRegs: numVRegs}
}

func TestRewriteNoSpills(t *testing.T) {
	prog := testProg(1,
		Instr{Op: CONST_INT, Dst: 1, Imm: 42, Type: T_INT64},
		Instr{Op: RETURN, Src1: 1, Type: T_INT64},
	)
	locs := map[VReg]Location{
		1: {Reg: 0, Reg2: -1, Slot: -1, Type: T_INT64},
	}
	out, err := Rewrite(prog, locs, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	if out != prog {
		t.Fatal("expected same program returned when no spills")
	}
}

func TestRewriteSrc1Spilled(t *testing.T) {
	prog := testProg(1,
		Instr{Op: CONST_INT, Dst: 1, Imm: 42, Type: T_INT64},
		Instr{Op: RETURN, Src1: 1, Type: T_INT64},
	)
	locs := map[VReg]Location{
		1: {Reg: -1, Reg2: -1, Slot: 0, Type: T_INT64},
	}
	out, err := Rewrite(prog, locs, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	// Expect: CONST_INT, SPILL_STORE, SPILL_LOAD, RETURN = 4 instrs.
	if len(out.Instrs) != 4 {
		t.Fatalf("expected 4 instrs, got %d: %v", len(out.Instrs), opsOf(out.Instrs))
	}
	if out.Instrs[0].Op != CONST_INT {
		t.Fatalf("expected CONST_INT at 0, got %v", out.Instrs[0].Op)
	}
	if out.Instrs[1].Op != SPILL_STORE {
		t.Fatalf("expected SPILL_STORE at 1, got %v", out.Instrs[1].Op)
	}
	if out.Instrs[1].Imm != 16 {
		t.Fatalf("expected spill store offset 16, got %d", out.Instrs[1].Imm)
	}
	if out.Instrs[2].Op != SPILL_LOAD {
		t.Fatalf("expected SPILL_LOAD at 2, got %v", out.Instrs[2].Op)
	}
	if out.Instrs[2].Imm != 16 {
		t.Fatalf("expected spill load offset 16, got %d", out.Instrs[2].Imm)
	}
	if out.Instrs[3].Op != RETURN {
		t.Fatalf("expected RETURN at 3, got %v", out.Instrs[3].Op)
	}
	if out.NumVRegs < 2 {
		t.Fatalf("expected NumVRegs >= 2, got %d", out.NumVRegs)
	}
}

func TestRewriteDstSpilled(t *testing.T) {
	prog := testProg(1,
		Instr{Op: CONST_INT, Dst: 1, Imm: 42, Type: T_INT64},
	)
	locs := map[VReg]Location{
		1: {Reg: -1, Reg2: -1, Slot: 0, Type: T_INT64},
	}
	out, err := Rewrite(prog, locs, 1, 8)
	if err != nil {
		t.Fatal(err)
	}
	// Expect: CONST_INT (dst rewritten), SPILL_STORE.
	if len(out.Instrs) != 2 {
		t.Fatalf("expected 2 instrs, got %d: %v", len(out.Instrs), opsOf(out.Instrs))
	}
	if out.Instrs[0].Op != CONST_INT {
		t.Fatalf("expected CONST_INT at 0, got %v", out.Instrs[0].Op)
	}
	if out.Instrs[0].Dst == 1 {
		t.Fatalf("expected dst to be rewritten from vreg 1")
	}
	if out.Instrs[1].Op != SPILL_STORE {
		t.Fatalf("expected SPILL_STORE at 1, got %v", out.Instrs[1].Op)
	}
	if out.Instrs[1].Imm != 8 {
		t.Fatalf("expected spill store offset 8, got %d", out.Instrs[1].Imm)
	}
}

func TestRewriteBothSrcsSpilled(t *testing.T) {
	prog := testProg(3,
		Instr{Op: ADD_INT, Dst: 3, Src1: 1, Src2: 2, Type: T_INT64},
	)
	locs := map[VReg]Location{
		1: {Reg: -1, Reg2: -1, Slot: 0, Type: T_INT64},
		2: {Reg: -1, Reg2: -1, Slot: 1, Type: T_INT64},
		3: {Reg: 0, Reg2: -1, Slot: -1, Type: T_INT64},
	}
	out, err := Rewrite(prog, locs, 2, 16)
	if err != nil {
		t.Fatal(err)
	}
	// Expect: SPILL_LOAD(src1), SPILL_LOAD(src2), ADD_INT = 3 instrs.
	if len(out.Instrs) != 3 {
		t.Fatalf("expected 3 instrs, got %d: %v", len(out.Instrs), opsOf(out.Instrs))
	}
	if out.Instrs[0].Op != SPILL_LOAD || out.Instrs[1].Op != SPILL_LOAD {
		t.Fatalf("expected two SPILL_LOADs, got %v %v", out.Instrs[0].Op, out.Instrs[1].Op)
	}
	// Fresh vregs should be distinct.
	if out.Instrs[0].Dst == out.Instrs[1].Dst {
		t.Fatalf("expected distinct fresh vregs for src1 and src2")
	}
}

func TestRewriteSameSrcReused(t *testing.T) {
	prog := testProg(2,
		Instr{Op: ADD_INT, Dst: 2, Src1: 1, Src2: 1, Type: T_INT64},
	)
	locs := map[VReg]Location{
		1: {Reg: -1, Reg2: -1, Slot: 0, Type: T_INT64},
		2: {Reg: 0, Reg2: -1, Slot: -1, Type: T_INT64},
	}
	out, err := Rewrite(prog, locs, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	// Only one SPILL_LOAD even though Src1==Src2, because the load is reused.
	if len(out.Instrs) != 2 {
		t.Fatalf("expected 2 instrs, got %d: %v", len(out.Instrs), opsOf(out.Instrs))
	}
	if out.Instrs[0].Op != SPILL_LOAD {
		t.Fatalf("expected SPILL_LOAD at 0, got %v", out.Instrs[0].Op)
	}
	if out.Instrs[1].Src1 != out.Instrs[1].Src2 {
		t.Fatalf("expected rewritten Src1==Src2, got %d vs %d", out.Instrs[1].Src1, out.Instrs[1].Src2)
	}
}

func TestRewriteBranchLabelFixup(t *testing.T) {
	prog := testProg(2,
		Instr{Op: CONST_INT, Dst: 1, Imm: 1, Type: T_INT64},
		Instr{Op: BR_TRUE, Src1: 1, Lbl: 3, Type: T_BOOL},
		Instr{Op: CONST_INT, Dst: 2, Imm: 2, Type: T_INT64},
		Instr{Op: RETURN, Src1: 2, Type: T_INT64},
	)
	locs := map[VReg]Location{
		1: {Reg: -1, Reg2: -1, Slot: 0, Type: T_INT64},
		2: {Reg: 0, Reg2: -1, Slot: -1, Type: T_INT64},
	}
	out, err := Rewrite(prog, locs, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	// Find the BR_TRUE and check its label was remapped.
	var brIdx int
	for i, ins := range out.Instrs {
		if ins.Op == BR_TRUE {
			brIdx = i
			break
		}
	}
	brLbl := int(out.Instrs[brIdx].Lbl)
	// The branch should target the RETURN instruction's new index.
	if brLbl < 0 || brLbl >= len(out.Instrs) {
		t.Fatalf("branch label %d out of range [0,%d)", brLbl, len(out.Instrs))
	}
	if out.Instrs[brLbl].Op != RETURN {
		t.Fatalf("expected branch to target RETURN, got %v at index %d", out.Instrs[brLbl].Op, brLbl)
	}
}

func TestRewriteStringType(t *testing.T) {
	prog := testProg(1,
		Instr{Op: CONST_STRING, Dst: 1, Imm: 0, Type: T_STRING},
		Instr{Op: RETURN, Src1: 1, Type: T_STRING},
	)
	locs := map[VReg]Location{
		1: {Reg: -1, Reg2: -1, Slot: 0, Type: T_STRING},
	}
	out, err := Rewrite(prog, locs, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	// Find the SPILL_LOAD for the RETURN's src and verify it has T_STRING type.
	for _, ins := range out.Instrs {
		if ins.Op == SPILL_LOAD {
			if ins.Type != T_STRING {
				t.Fatalf("expected SPILL_LOAD type T_STRING, got %v", ins.Type)
			}
			return
		}
	}
	t.Fatal("expected SPILL_LOAD for string source")
}

func opsOf(instrs []Instr) []Opcode {
	ops := make([]Opcode, len(instrs))
	for i, ins := range instrs {
		ops[i] = ins.Op
	}
	return ops
}
