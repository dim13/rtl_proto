module github.com/dim13/rtl_proto

go 1.27.4

tool (
	google.golang.org/grpc/cmd/protoc-gen-go-grpc
	google.golang.org/protobuf/cmd/protoc-gen-go
)

require (
	github.com/dim13/r82xx v0.0.0-20261122174307-2ba683df77ae
	github.com/google/gousb v1.1.3
	google.golang.org/grpc v1.81.0
	google.golang.org/protobuf v1.36.12
)

require (
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171 // indirect
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.6.2 // indirect
)
