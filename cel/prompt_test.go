// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cel

import (
	_ "embed"
	"testing"

	"github.com/taichimaeda/cel-go/common/env"
	"github.com/taichimaeda/cel-go/test"
)

//go:embed testdata/basic.prompt.txt
var wantBasicPrompt string

//go:embed testdata/macros.prompt.txt
var wantMacrosPrompt string

//go:embed testdata/standard_env.prompt.txt
var wantStandardEnvPrompt string

func TestPromptTemplate(t *testing.T) {
	tests := []struct {
		name    string
		envOpts []EnvOption
		out     string
	}{
		{
			name: "basic",
			out:  wantBasicPrompt,
		},
		{
			name:    "macros",
			envOpts: []EnvOption{Macros(StandardMacros...)},
			out:     wantMacrosPrompt,
		},
		{
			name:    "standard_env",
			envOpts: []EnvOption{StdLib(StdLibSubset(env.NewLibrarySubset().SetDisableMacros(true)))},
			out:     wantStandardEnvPrompt,
		},
	}

	for _, tst := range tests {
		tc := tst
		t.Run(tc.name, func(t *testing.T) {
			env, err := NewCustomEnv(tc.envOpts...)
			if err != nil {
				t.Fatalf("cel.NewCustomEnv() failed: %v", err)
			}
			prompt, err := AuthoringPrompt(env)
			if err != nil {
				t.Fatalf("cel.AuthoringPrompt() failed: %v", err)
			}
			out := prompt.Render("<USER_PROMPT>")
			if !test.Compare(out, tc.out) {
				t.Errorf("got %s, wanted %s", out, tc.out)
			}
		})
	}
}
