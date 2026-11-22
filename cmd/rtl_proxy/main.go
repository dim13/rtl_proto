// Command rtl_proxy serves rtl_tcp clients from a remote rtl_remote.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/dim13/rtl_proto/discovery"
	"github.com/dim13/rtl_proto/proxy"
	"github.com/dim13/rtl_proto/remote"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// remoteAddr adds the default port to a bare host.
func remoteAddr(s string) string {
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return net.JoinHostPort(strings.Trim(s, "[]"), strconv.Itoa(discovery.Port))
}

// listen listens on every address a host name resolves to, on one
// port: "localhost" may resolve to ::1 first, and some clients try only
// the first address.
func listen(addr string) ([]net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if host == "" || net.ParseIP(host) != nil {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		return []net.Listener{l}, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}
	var ls []net.Listener
	for _, ip := range ips {
		l, err := net.Listen("tcp", net.JoinHostPort(ip.String(), port))
		if err != nil {
			slog.Warn("listen", "ip", ip, "err", err)
			continue
		}
		ls = append(ls, l)
		// Port 0: the remaining addresses take the port just picked.
		_, port, _ = net.SplitHostPort(l.Addr().String())
	}
	if len(ls) == 0 {
		return nil, fmt.Errorf("no address to listen on for %s", host)
	}
	return ls, nil
}

// findRemote returns the address given as argument, or else asks the
// local network.
func findRemote(arg string) (string, error) {
	if arg != "" {
		return remoteAddr(arg), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addr, err := discovery.Find(ctx, discovery.Broadcast)
	if err != nil {
		return "", fmt.Errorf("%w; give host as argument", err)
	}
	return addr, nil
}

func main() {
	var (
		addr    = flag.String("a", "localhost:1234", "rtl_tcp listen address")
		minBits = flag.Uint("min-bits", 2, "lowest bits per sample component, 1..8")
		maxBits = flag.Uint("max-bits", 8, "highest bits per sample component, 1..8")
		debug   = flag.Bool("d", false, "log received commands and stream stats")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: rtl_proxy [flags] [host[:port]]\nWithout host, the remote is found on the local network.\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	var level slog.Level
	if *debug {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
	slog.SetLogLoggerLevel(slog.LevelError)

	raddr, err := findRemote(flag.Arg(0))
	if err != nil {
		log.Fatal("remote: ", err)
	}
	cc, err := grpc.NewClient(raddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(remote.Window),
		grpc.WithInitialConnWindowSize(remote.Window),
		// Reconnect within seconds after the remote host comes back,
		// instead of gRPC's default backoff of up to two minutes.
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  250 * time.Millisecond,
				Multiplier: 1.6,
				MaxDelay:   2 * time.Second,
			},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                5 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		log.Fatal("dial: ", err)
	}
	defer cc.Close()
	p := proxy.New(pb.NewReceiverClient(cc), proxy.Options{
		MinBits: uint32(*minBits),
		MaxBits: uint32(*maxBits),
	})

	ls, err := listen(*addr)
	if err != nil {
		log.Fatal("listen: ", err)
	}
	conns := make(chan net.Conn)
	for _, l := range ls {
		slog.Info("listening", "addr", l.Addr(), "remote", raddr)
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					log.Fatal("accept: ", err)
				}
				conns <- conn
			}
		}()
	}
	// One client at a time, like rtl_tcp; others wait.
	for conn := range conns {
		slog.Info("client", "addr", conn.RemoteAddr())
		if err := p.Serve(context.Background(), conn); err != nil {
			slog.Error("session", "err", err)
		}
	}
}
