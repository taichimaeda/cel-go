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

// CallerSave describes a physical register that must be saved/restored
// around a CALL_BUILTIN instruction because it is caller-saved and live across the call.
type CallerSave struct {
	Reg     int16 // Primary physical register.
	Reg2    int16 // Secondary register for string pairs (-1 if unused).
	Slot    int   // Sequential save slot index for this entry.
	IsFloat bool  // True if this is a float register (Reg >= 100).
}

// computeCallerSaves determines which physical registers need saving around each
// CALL_BUILTIN instruction. A register needs saving when:
//   - it belongs to a vreg that is defined before the call and used (as source) after
//   - it is not the destination of the call itself
//   - it is allocated to a caller-saved physical register
//
// Returns the per-call save map and the maximum number of save slots needed
// across all calls.
func computeCallerSaves(instrs []Instr, vregLocs map[VReg]Location, calleeSet map[int16]bool) (map[int][]CallerSave, int) {
	firstDefs, lastUse := computeLiveness(instrs)

	result := make(map[int][]CallerSave)
	maxSlots := 0
	for i, ins := range instrs {
		if ins.Op != CALL_BUILTIN {
			continue
		}
		saves, slots := computeCallerSave(i, ins, firstDefs, lastUse, vregLocs, calleeSet)
		if len(saves) > 0 {
			result[i] = saves
			if slots > maxSlots {
				maxSlots = slots
			}
		}
	}
	return result, maxSlots
}

// computeCallerSave computes the save entries for a single CALL_BUILTIN at index callIdx.
func computeCallerSave(
	callIdx int,
	ins Instr,
	firstDefs map[VReg]int,
	lastUses map[VReg]int,
	vregLocs map[VReg]Location,
	calleeSet map[int16]bool,
) ([]CallerSave, int) {
	var saves []CallerSave
	slot := 0
	for vreg, loc := range vregLocs {
		if vreg == ins.Dst {
			continue
		}
		if loc.Slot >= 0 || loc.Reg < 0 {
			continue
		}
		def, hasDef := firstDefs[vreg]
		use, hasUse := lastUses[vreg]
		if !hasDef || !hasUse || !(def <= callIdx && callIdx < use) {
			continue
		}
		// Check if any assigned physical register is caller-saved.
		r1Caller := !calleeSet[loc.Reg]
		r2Caller := loc.Reg2 >= 0 && !calleeSet[loc.Reg2]
		if !r1Caller && !r2Caller {
			continue
		}
		isFloat := loc.Reg >= 100
		saves = append(saves, CallerSave{
			Reg:     loc.Reg,
			Reg2:    loc.Reg2,
			Slot:    slot,
			IsFloat: isFloat,
		})
		if loc.Reg2 >= 0 {
			slot += 2
		} else {
			slot++
		}
	}
	return saves, slot
}

// computeLiveness returns first-definition and last-source-use indices for each vreg.
func computeLiveness(instrs []Instr) (firstDefs map[VReg]int, lastUse map[VReg]int) {
	firstDefs = make(map[VReg]int)
	lastUse = make(map[VReg]int)
	seen := make(map[VReg]bool)
	for i, ins := range instrs {
		if ins.Dst != 0 && !seen[ins.Dst] {
			firstDefs[ins.Dst] = i
			seen[ins.Dst] = true
		}
		if ins.Src1 != 0 {
			lastUse[ins.Src1] = i
		}
		if ins.Src2 != 0 {
			lastUse[ins.Src2] = i
		}
	}
	return firstDefs, lastUse
}
