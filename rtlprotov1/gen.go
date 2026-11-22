// Package rtlprotov1 holds the generated rtlproto.v1 messages and gRPC
// service.
package rtlprotov1

//go:generate protoc -I ../proto --go_out=.. --go_opt=module=github.com/dim13/rtl_proto --go-grpc_out=.. --go-grpc_opt=module=github.com/dim13/rtl_proto rtlproto/v1/receiver.proto
