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

func TestComputeCallerSaves_NoCallsNoSaves(t *testing.T) {
	instrs := []Instr{
		{Op: CONST_INT, Dst: 1, Imm: 42, Type: T_INT64},
		{Op: RETURN, Src1: 1, Type: T_BOOL},
	}
	vregLocs := map[VReg]Location{
		1: {Reg: 0, Reg2: -1, Slot: -1},
	}
	calleeSet := map[int16]bool{}
	saves, maxSaves := computeCallerSaves(instrs, vregLocs, calleeSet)
	if len(saves) != 0 {
		t.Fatalf("expected no saves, got %d call sites", len(saves))
	}
	if maxSaves != 0 {
		t.Fatalf("expected 0 max slots, got %d", maxSaves)
	}
}

func TestComputeCallerSaves_LiveAcrossCall(t *testing.T) {
	// Simulates: needle = LOAD_FIELD; lit = CONST_STRING; cmp = CALL_BUILTIN(needle, lit); ... use needle again
	instrs := []Instr{
		{Op: LOAD_FIELD, Dst: 1, Imm: 0, Type: T_STRING},          // 0: needle
		{Op: CONST_STRING, Dst: 2, Imm: 0, Type: T_STRING},        // 1: lit "a"
		{Op: CALL_BUILTIN, Dst: 3, Src1: 1, Src2: 2, Type: T_BOOL, BuiltinID: BuiltinStrEq}, // 2: call
		{Op: BR_TRUE, Src1: 3},                                     // 3
		{Op: CONST_STRING, Dst: 4, Imm: 1, Type: T_STRING},        // 4: lit "b"
		{Op: CALL_BUILTIN, Dst: 5, Src1: 1, Src2: 4, Type: T_BOOL, BuiltinID: BuiltinStrEq}, // 5: call
		{Op: BR_TRUE, Src1: 5},                                     // 6
		{Op: CONST_BOOL, Dst: 6, Imm: 0, Type: T_BOOL},            // 7
		{Op: RETURN, Src1: 6, Type: T_BOOL},                        // 8
	}
	// needle (vreg 1) allocated to caller-saved regs 0,1 (string pair).
	vregLocs := map[VReg]Location{
		1: {Reg: 0, Reg2: 1, Slot: -1},  // needle: caller-saved
		2: {Reg: 2, Reg2: 3, Slot: -1},  // lit a
		3: {Reg: 6, Reg2: -1, Slot: -1}, // cmp result
		4: {Reg: 2, Reg2: 3, Slot: -1},  // lit b (reuse after dead)
		5: {Reg: 7, Reg2: -1, Slot: -1}, // cmp result 2
		6: {Reg: 8, Reg2: -1, Slot: -1}, // final bool
	}
	// Only reg 12 is callee-saved on amd64; everything else is caller-saved.
	calleeSet := map[int16]bool{12: true}

	saves, maxSaves := computeCallerSaves(instrs, vregLocs, calleeSet)

	// Call at index 2: needle (vreg 1, regs 0+1) is live across (used at index 5).
	entries2, ok := saves[2]
	if !ok {
		t.Fatal("expected save entries for call at index 2")
	}
	if len(entries2) != 1 {
		t.Fatalf("expected 1 save entry at call 2, got %d", len(entries2))
	}
	if entries2[0].Reg != 0 || entries2[0].Reg2 != 1 {
		t.Fatalf("expected regs 0,1 for needle save, got %d,%d", entries2[0].Reg, entries2[0].Reg2)
	}
	if entries2[0].Slot != 0 {
		t.Fatalf("expected slot 0, got %d", entries2[0].Slot)
	}

	// Call at index 5: needle (vreg 1) last use is at index 5 (as Src1).
	// Since lastUses[1] == 5 and call is at 5, use <= callIdx, so NOT live across.
	if _, ok := saves[5]; ok {
		t.Fatal("expected no save entries for call at index 5 (needle's last use)")
	}

	if maxSaves != 2 {
		t.Fatalf("expected maxSaves=2 (string pair), got %d", maxSaves)
	}
}

func TestComputeCallerSaves_CalleeSavedNotSaved(t *testing.T) {
	instrs := []Instr{
		{Op: CONST_INT, Dst: 1, Imm: 10, Type: T_INT64},
		{Op: CALL_BUILTIN, Dst: 2, Src1: 1, Type: T_BOOL, BuiltinID: BuiltinStrSize},
		{Op: RETURN, Src1: 1, Type: T_BOOL}, // vreg 1 used after call
	}
	vregLocs := map[VReg]Location{
		1: {Reg: 12, Reg2: -1, Slot: -1}, // callee-saved
		2: {Reg: 0, Reg2: -1, Slot: -1},
	}
	calleeSet := map[int16]bool{12: true}

	saves, maxSaves := computeCallerSaves(instrs, vregLocs, calleeSet)
	if len(saves) != 0 {
		t.Fatalf("expected no saves for callee-saved reg, got %d entries", len(saves))
	}
	if maxSaves != 0 {
		t.Fatalf("expected 0 max slots, got %d", maxSaves)
	}
}

func TestComputeCallerSaves_DstNotSaved(t *testing.T) {
	instrs := []Instr{
		{Op: CONST_INT, Dst: 1, Imm: 10, Type: T_INT64},
		{Op: CALL_BUILTIN, Dst: 1, Src1: 1, Type: T_BOOL, BuiltinID: BuiltinStrSize}, // overwrites vreg 1
		{Op: RETURN, Src1: 1, Type: T_BOOL},
	}
	vregLocs := map[VReg]Location{
		1: {Reg: 0, Reg2: -1, Slot: -1},
	}
	calleeSet := map[int16]bool{12: true}

	saves, _ := computeCallerSaves(instrs, vregLocs, calleeSet)
	if entries, ok := saves[1]; ok {
		t.Fatalf("expected Dst vreg not saved, but got %d entries", len(entries))
	}
}

func TestComputeLiveness(t *testing.T) {
	instrs := []Instr{
		{Op: CONST_INT, Dst: 1, Imm: 42, Type: T_INT64},    // 0
		{Op: CONST_INT, Dst: 2, Imm: 10, Type: T_INT64},    // 1
		{Op: CALL_BUILTIN, Dst: 3, Src1: 1, Src2: 2, Type: T_BOOL}, // 2
		{Op: RETURN, Src1: 1, Type: T_BOOL},                 // 3
	}
	firstDefs, lastUses := computeLiveness(instrs)

	if firstDefs[1] != 0 {
		t.Fatalf("firstDefs[1] = %d, want 0", firstDefs[1])
	}
	if firstDefs[2] != 1 {
		t.Fatalf("firstDefs[2] = %d, want 1", firstDefs[2])
	}
	if firstDefs[3] != 2 {
		t.Fatalf("firstDefs[3] = %d, want 2", firstDefs[3])
	}
	// vreg 1 used as Src1 at index 2 and Src1 at index 3.
	if lastUses[1] != 3 {
		t.Fatalf("lastUses[1] = %d, want 3", lastUses[1])
	}
	// vreg 2 used as Src2 at index 2 only.
	if lastUses[2] != 2 {
		t.Fatalf("lastUses[2] = %d, want 2", lastUses[2])
	}
}
