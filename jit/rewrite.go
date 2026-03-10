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

// rewriter holds mutable state for a single Rewrite pass.
type rewriter struct {
	newInstrs []Instr
	nextVReg  VReg
	vregLocs  map[VReg]Location
	spillOff  int
	indexMap  []int
}

// Rewrite rewrites the instruction stream to replace spilled vreg references
// with explicit SPILL_LOAD/SPILL_STORE instructions using fresh virtual
// registers.
//
// For each instruction that uses a spilled vreg as a source, a SPILL_LOAD is
// inserted before it, loading the value from the spill slot into a fresh vreg.
// For each instruction that defines a spilled vreg, the destination is replaced
// with a fresh vreg and a SPILL_STORE is inserted after.
//
// The fresh vregs are not assigned physical registers here; a subsequent
// register allocation pass handles that. Returns the rewritten instruction
// stream and the new NumVRegs count.
func Rewrite(
	prog *Program,
	vregLocs map[VReg]Location,
	spillSlots int,
	spillOff int,
) (*Program, error) {
	if spillSlots == 0 {
		return prog, nil
	}

	n := len(prog.Instrs)
	rw := &rewriter{
		newInstrs: make([]Instr, 0, n+spillSlots*2),
		nextVReg:  VReg(prog.NumVRegs + 1),
		vregLocs:  vregLocs,
		spillOff:  spillOff,
		indexMap:  make([]int, n+1),
	}

	rw.rewriteInstrs(prog.Instrs)
	rw.fixupBranchTargets(prog.Instrs)

	return &Program{
		Instrs:     rw.newInstrs,
		NumVRegs:   int(rw.nextVReg - 1),
		StringPool: prog.StringPool,
	}, nil
}

// rewriteInstrs iterates over the original instruction stream, inserting
// SPILL_LOAD/SPILL_STORE pseudo-ops around instructions that reference
// spilled vregs. Populates rw.indexMap for branch label fixup.
func (rw *rewriter) rewriteInstrs(instrs []Instr) {
	for i, ins := range instrs {
		rw.indexMap[i] = len(rw.newInstrs)
		rw.spillInstr(ins)
	}
	rw.indexMap[len(instrs)] = len(rw.newInstrs)
}

// spillInstr handles one original instruction: emits SPILL_LOADs for spilled
// sources, rewrites the instruction operands, and emits a SPILL_STORE for a
// spilled destination.
func (rw *rewriter) spillInstr(ins Instr) {
	newSrc1 := rw.spillLoadSrc1(ins.Src1)
	newSrc2 := rw.spillLoadSrc2(ins, newSrc1)

	newIns := ins
	newIns.Src1 = newSrc1
	newIns.Src2 = newSrc2
	rw.newInstrs = append(rw.newInstrs, newIns)

	rw.spillStoreDst(ins)
}

// spillLoadSrc1 emits a SPILL_LOAD if vreg is spilled, returning the
// replacement vreg (or the original if not spilled).
func (rw *rewriter) spillLoadSrc1(v VReg) VReg {
	if v == 0 {
		return v
	}
	loc := rw.vregLocs[v]
	if loc.Slot < 0 {
		return v
	}
	off := int64(rw.spillOff + loc.Slot*8)
	tmpV := rw.nextVReg
	rw.nextVReg++
	rw.newInstrs = append(rw.newInstrs, Instr{
		Op: SPILL_LOAD, Dst: tmpV, Imm: off, Type: loc.Type,
	})
	return tmpV
}

// spillLoadSrc2 handles Src2 spill loading, including the same-vreg reuse
// case where Src1 == Src2.
func (rw *rewriter) spillLoadSrc2(ins Instr, newSrc1 VReg) VReg {
	if ins.Src2 == 0 {
		return 0
	}
	if ins.Src2 == ins.Src1 && rw.vregLocs[ins.Src2].Slot >= 0 {
		// Same vreg as Src1, already loaded.
		return newSrc1
	}
	if ins.Src2 == ins.Src1 {
		return ins.Src2
	}
	return rw.spillLoadSrc1(ins.Src2)
}

// spillStoreDst replaces the most recently appended instruction's Dst with a
// fresh vreg and emits a SPILL_STORE if Dst was spilled.
func (rw *rewriter) spillStoreDst(ins Instr) {
	if ins.Dst == 0 {
		return
	}
	loc := rw.vregLocs[ins.Dst]
	if loc.Slot < 0 {
		return
	}
	tmpV := rw.nextVReg
	rw.nextVReg++
	rw.newInstrs[len(rw.newInstrs)-1].Dst = tmpV
	off := int64(rw.spillOff + loc.Slot*8)
	rw.newInstrs = append(rw.newInstrs, Instr{
		Op: SPILL_STORE, Src1: tmpV, Imm: off, Type: loc.Type,
	})
}

// fixupBranchTargets remaps branch target indices from old instruction positions
// to new positions after spill instruction insertion.
func (rw *rewriter) fixupBranchTargets(oldInstrs []Instr) {
	for i := range rw.newInstrs {
		switch rw.newInstrs[i].Op {
		case BR, BR_TRUE, BR_FALSE:
			oldLbl := int(rw.newInstrs[i].Lbl)
			if oldLbl >= 0 && oldLbl <= len(oldInstrs) {
				rw.newInstrs[i].Lbl = Label(rw.indexMap[oldLbl])
			}
		}
	}
}
