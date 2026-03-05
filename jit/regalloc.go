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
	"sort"
)

// Interval models the live range for a virtual register.
type Interval struct {
	VReg  VReg
	Type  Type
	Start int
	End   int
}

// Location is the final assigned location for a VReg (register or spill slot).
type Location struct {
	Reg       int16
	Reg2      int16
	SpillSlot int
}

// Allocate runs linear-scan allocation for the current runtime architecture.
func Allocate(p *Program) map[VReg]Location {
	return newAllocator(p).allocate()
}

type allocator struct {
	allIntervals    []Interval
	activeIntervals []Interval
	alloc           map[VReg]Location
	freeInt         []int16
	freeFloat       []int16
	calleeSet       map[int16]bool
	usedCallee      map[int16]bool
	nextSpill       int
}

func newAllocator(p *Program) *allocator {
	intervals := computeIntervals(p)
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].Start != intervals[j].Start {
			return intervals[i].Start < intervals[j].Start
		}
		return intervals[i].VReg < intervals[j].VReg
	})

	intRegs, floatRegs, calleeSet := buildRegSets()
	freeInt := append([]int16(nil), intRegs...)
	freeFloat := append([]int16(nil), floatRegs...)
	sort.Slice(freeInt, func(i, j int) bool { return freeInt[i] < freeInt[j] })
	sort.Slice(freeFloat, func(i, j int) bool { return freeFloat[i] < freeFloat[j] })

	return &allocator{
		allIntervals:    intervals,
		activeIntervals: make([]Interval, 0, len(intervals)),
		alloc:           make(map[VReg]Location, len(intervals)),
		freeInt:         freeInt,
		freeFloat:       freeFloat,
		calleeSet:       calleeSet,
		usedCallee:      make(map[int16]bool),
	}
}

func computeIntervals(p *Program) []Interval {
	seen := make(map[VReg]bool)
	typeMap := make(map[VReg]Type)
	intervalMap := make(map[VReg]Interval)

	for i, ins := range p.Instrs {
		if ins.Dst != 0 {
			iv := getInterval(intervalMap, ins.Dst, i)
			if !seen[ins.Dst] {
				iv.Start = i
				seen[ins.Dst] = true
			}
			if i > iv.End {
				iv.End = i
			}
			if ins.Type != T_UNSPECIFIED {
				iv.Type = ins.Type
				typeMap[ins.Dst] = ins.Type
			}
			intervalMap[ins.Dst] = iv
		}
		for _, src := range []VReg{ins.Src1, ins.Src2} {
			if src == 0 {
				continue
			}
			iv := getInterval(intervalMap, src, i)
			if !seen[src] {
				iv.Start = i
				seen[src] = true
			}
			if i > iv.End {
				iv.End = i
			}
			if iv.Type == T_UNSPECIFIED {
				if t, found := typeMap[src]; found {
					iv.Type = t
				}
			}
			intervalMap[src] = iv
		}
	}

	intervals := make([]Interval, 0, len(intervalMap))
	for _, iv := range intervalMap {
		if iv.Type == T_UNSPECIFIED {
			iv.Type = T_INT64
		}
		intervals = append(intervals, iv)
	}
	return intervals
}

func getInterval(intervalMap map[VReg]Interval, v VReg, idx int) Interval {
	iv, found := intervalMap[v]
	if !found {
		return Interval{
			VReg:  v,
			Start: idx,
			End:   idx,
		}
	}
	return iv
}

func insertInterval(intervals []Interval, iv Interval) []Interval {
	// Active intervals are kept sorted by End.
	pos := len(intervals)
	for i, a := range intervals {
		if iv.End < a.End {
			pos = i
			break
		}
	}
	intervals = append(intervals, Interval{})
	copy(intervals[pos+1:], intervals[pos:])
	intervals[pos] = iv
	return intervals
}

func (a *allocator) allocate() map[VReg]Location {
	for _, current := range a.allIntervals {
		a.dropInactiveIntervals(current.Start)
		a.sortActiveIntervals()
		loc, ok := a.tryAllocate(current.Type)
		if !ok {
			a.spill(current)
			continue
		}
		a.alloc[current.VReg] = loc
		a.activeIntervals = append(a.activeIntervals, current)
		a.markCallee(loc.Reg)
		a.markCallee(loc.Reg2)
	}
	if a.nextSpill != 0 {
		return nil
	}
	if err := a.validateAlloc(); err != nil {
		return nil
	}
	return a.alloc
}

func (a *allocator) tryAllocate(typ Type) (Location, bool) {
	switch typ {
	case T_FLOAT64:
		if len(a.freeFloat) == 0 {
			return Location{}, false
		}
		r := a.freeFloat[0]
		a.freeFloat = a.freeFloat[1:]
		return Location{Reg: r, Reg2: -1, SpillSlot: -1}, true
	case T_STRING:
		if len(a.freeInt) < 2 {
			return Location{}, false
		}
		for i := 0; i < len(a.freeInt)-1; i++ {
			r1, r2 := a.freeInt[i], a.freeInt[i+1]
			if r2 == r1+1 {
				a.freeInt = append(a.freeInt[:i], a.freeInt[i+2:]...)
				return Location{Reg: r1, Reg2: r2, SpillSlot: -1}, true
			}
		}
		return Location{}, false
	default:
		if len(a.freeInt) == 0 {
			return Location{}, false
		}
		r := a.freeInt[0]
		a.freeInt = a.freeInt[1:]
		return Location{Reg: r, Reg2: -1, SpillSlot: -1}, true
	}
}

func (a *allocator) spill(current Interval) {
	// Find the active interval with the latest End.
	farthestIdx := -1
	farthestEnd := -1
	for j, iv := range a.activeIntervals {
		if iv.End > farthestEnd {
			farthestEnd = iv.End
			farthestIdx = j
		}
	}
	if farthestIdx >= 0 && a.activeIntervals[farthestIdx].End > current.End {
		// Spill the farthest active interval.
		victim := a.activeIntervals[farthestIdx]
		a.alloc[current.VReg] = a.alloc[victim.VReg]
		a.alloc[victim.VReg] = a.nextSpillLocation(victim.Type)
		a.activeIntervals = append(a.activeIntervals[:farthestIdx], a.activeIntervals[farthestIdx+1:]...)
		a.activeIntervals = insertInterval(a.activeIntervals, current)
		return
	}
	// Spill current.
	a.alloc[current.VReg] = a.nextSpillLocation(current.Type)
}

func (a *allocator) nextSpillLocation(t Type) Location {
	loc := Location{Reg: -1, Reg2: -1, SpillSlot: a.nextSpill}
	a.nextSpill++
	if t == T_STRING {
		a.nextSpill++
	}
	return loc
}

func (a *allocator) dropInactiveIntervals(start int) {
	var kept []Interval
	for _, iv := range a.activeIntervals {
		if iv.End < start {
			loc := a.alloc[iv.VReg]
			if loc.SpillSlot >= 0 {
				continue
			}
			a.markFree(loc.Reg)
			a.markFree(loc.Reg2)
			continue
		}
		kept = append(kept, iv)
	}
	a.activeIntervals = kept
	sort.Slice(a.freeInt, func(i, j int) bool { return a.freeInt[i] < a.freeInt[j] })
	sort.Slice(a.freeFloat, func(i, j int) bool { return a.freeFloat[i] < a.freeFloat[j] })
}

func (a *allocator) sortActiveIntervals() {
	sort.Slice(a.activeIntervals, func(i, j int) bool {
		return a.activeIntervals[i].End < a.activeIntervals[j].End
	})
}

func (a *allocator) markFree(reg int16) {
	if reg < 0 {
		return
	}
	// Float register IDs are disjoint from integer register IDs.
	if reg >= 100 {
		a.freeFloat = append(a.freeFloat, reg)
		return
	}
	a.freeInt = append(a.freeInt, reg)
}

func (a *allocator) markCallee(reg int16) {
	if reg < 0 {
		return
	}
	if a.calleeSet[reg] {
		a.usedCallee[reg] = true
	}
}

func (a *allocator) validateAlloc() error {
	for vreg, loc := range a.alloc {
		if loc.SpillSlot >= 0 || loc.Reg < 0 {
			return fmt.Errorf("%w: vreg v%d has invalid primary location (spill_slot=%d reg=%d)", ErrCodegenUnsupported, vreg, loc.SpillSlot, loc.Reg)
		}
		if loc.Reg < 100 {
			if !isAllocIntReg(loc.Reg) {
				return fmt.Errorf("%w: vreg v%d assigned integer register %d outside allocatable set", ErrCodegenUnsupported, vreg, loc.Reg)
			}
		} else if !isAllocFloatReg(loc.Reg) {
			return fmt.Errorf("%w: vreg v%d assigned float register %d outside allocatable set", ErrCodegenUnsupported, vreg, loc.Reg)
		}
		if loc.Reg2 >= 0 && !isAllocIntReg(loc.Reg2) {
			return fmt.Errorf("%w: vreg v%d assigned second register %d outside allocatable integer set", ErrCodegenUnsupported, vreg, loc.Reg2)
		}
	}
	return nil
}
