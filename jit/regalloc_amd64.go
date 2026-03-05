//go:build amd64

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
	intRegs = []int16{0, 1, 2, 3, 6, 7, 8, 9, 10, 11, 12}
	floatRegs = make([]int16, 0, 16)
	for r := int16(0); r <= 15; r++ {
		// Prefix float regs with +100 to prevent collision.
		floatRegs = append(floatRegs, r+100)
	}
	calleeSet = make(map[int16]bool)
	calleeSet[12] = true
	for r := int16(8); r <= 15; r++ {
		calleeSet[r+100] = true
	}
	return intRegs, floatRegs, calleeSet
}

func isAllocIntReg(reg int16) bool {
	return reg == 0 || reg == 1 || reg == 2 || reg == 3 ||
		reg == 6 || reg == 7 || reg == 8 || reg == 9 ||
		reg == 10 || reg == 11 || reg == 12
}

func isAllocFloatReg(reg int16) bool {
	return reg >= 100 && reg <= 115
}
