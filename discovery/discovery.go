// Package discovery lets rtl_proxy find rtl_remote on the local network
// without configuration: the proxy broadcasts a query over UDP, the
// remote answers with its gRPC port, and the answer's source address
// gives the host.
//
// Query: the ASCII bytes "rtl_proto?". Answer: "rtl_proto <port>".
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var ErrNotFound = errors.New("no remote found")

const (
	query  = "rtl_proto?"
	answer = "rtl_proto "
)

// Port is the default port, shared by discovery (UDP) and gRPC (TCP).
const Port = 7373

// Broadcast is where Find sends its query by default.
var Broadcast = fmt.Sprintf("255.255.255.255:%d", Port)

// Serve answers queries on pc with grpcPort until pc is closed.
func Serve(pc net.PacketConn, grpcPort int) error {
	buf := make([]byte, 64)
	reply := []byte(answer + strconv.Itoa(grpcPort))
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		if string(buf[:n]) == query {
			pc.WriteTo(reply, addr)
		}
	}
}

// Find sends a query to target (usually Broadcast) and returns the
// first answering remote as host:port.
func Find(ctx context.Context, target string) (string, error) {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return "", err
	}
	defer pc.Close()
	dst, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return "", err
	}
	if _, err := pc.WriteTo([]byte(query), dst); err != nil {
		return "", err
	}
	if d, ok := ctx.Deadline(); ok {
		pc.SetReadDeadline(d)
	}
	buf := make([]byte, 64)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return "", ErrNotFound
		}
		port, ok := strings.CutPrefix(string(buf[:n]), answer)
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(port); err != nil {
			continue
		}
		host, _, _ := net.SplitHostPort(addr.String())
		return net.JoinHostPort(host, port), nil
	}
}
