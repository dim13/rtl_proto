// Command rtl_proxy serves rtl_tcp clients from a remote rtl_remote.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/dim13/rtl_proto/proxy"
	"github.com/dim13/rtl_proto/remote"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// defaultPort is rtl_remote's default listen port.
const defaultPort = "7373"

// remoteAddr adds the default port to a bare host.
func remoteAddr(s string) string {
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return net.JoinHostPort(strings.Trim(s, "[]"), defaultPort)
}

func main() {
	var (
		raddr   = flag.String("r", "", "remote host[:port] (required, default port "+defaultPort+")")
		addr    = flag.String("a", "127.0.0.1:1234", "rtl_tcp listen address")
		minBits = flag.Uint("min-bits", 2, "lowest bits per sample component, 1..8")
		maxBits = flag.Uint("max-bits", 8, "highest bits per sample component, 1..8")
		debug   = flag.Bool("d", false, "log received commands and stream stats")
	)
	flag.Parse()
	if *raddr == "" {
		flag.Usage()
		os.Exit(2)
	}

	var level slog.Level
	if *debug {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
	slog.SetLogLoggerLevel(slog.LevelError)

	*raddr = remoteAddr(*raddr)
	cc, err := grpc.NewClient(*raddr,
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

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal("listen: ", err)
	}
	slog.Info("listening", "addr", l.Addr(), "remote", *raddr)
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Fatal("accept: ", err)
		}
		slog.Info("client", "addr", conn.RemoteAddr())
		if err := p.Serve(context.Background(), conn); err != nil {
			slog.Error("session", "err", err)
		}
	}
}
