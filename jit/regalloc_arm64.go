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

func buildRegSets() (intRegs []int16, floatRegs []int16, calleeSet map[int16]bool) {
	// R19 is reserved by the JIT as the input-base register.
	// Prefer caller-saved regs first to reduce pressure, then use callee-saved.
	intRegs = make([]int16, 0, 22)
	for r := int16(0); r <= 15; r++ {
		intRegs = append(intRegs, r)
	}
	for r := int16(20); r <= 25; r++ {
		intRegs = append(intRegs, r)
	}
	floatRegs = make([]int16, 0, 24)
	for r := int16(0); r <= 23; r++ {
		// Prefix float regs with +100 to prevent collision.
		floatRegs = append(floatRegs, r+100)
	}
	calleeSet = make(map[int16]bool)
	for r := int16(20); r <= 25; r++ {
		calleeSet[r] = true
	}
	for r := int16(16); r <= 23; r++ {
		calleeSet[r+100] = true
	}
	return intRegs, floatRegs, calleeSet
}

func isAllocIntReg(reg int16) bool {
	return (reg >= 0 && reg <= 15) || (reg >= 20 && reg <= 25)
}

func isAllocFloatReg(reg int16) bool {
	return reg >= 100 && reg <= 123
}
