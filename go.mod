module github.com/google/cel-go

go 1.23.0

require (
	cel.dev/expr v0.25.1
	github.com/antlr4-go/antlr/v4 v4.13.1
	github.com/bytedance/sonic/loader v0.0.0-00010101000000-000000000000
	go.yaml.in/yaml/v3 v3.0.4
	google.golang.org/genproto/googleapis/api v0.0.0-20240826202546-f6391c0de4c7
	google.golang.org/protobuf v1.36.10
)

require (
	github.com/twitchyliquid64/golang-asm v0.15.1
	golang.org/x/exp v0.0.0-20240823005443-9b4947da3948 // indirect
	golang.org/x/text v0.22.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240826202546-f6391c0de4c7 // indirect
)

// Pin sonic/loader to the arm64-enabled fork branch commit.
replace github.com/bytedance/sonic/loader => github.com/taichimaeda/sonic/loader v0.0.0-20260305131447-47ca97d0690b
