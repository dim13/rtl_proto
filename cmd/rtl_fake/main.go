// Command rtl_fake serves the rtlproto.v1 Receiver service on a fake
// device producing a test tone, for testing clients without hardware.
package main

import (
	stdgzip "compress/gzip"
	"context"
	"flag"
	"log"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/dim13/r82xx/tuner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/remote"
	"github.com/dim13/rtl_proto/remote/fake"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

func main() {
	var (
		addr     = flag.String("a", "127.0.0.1:0", "listen address")
		tone     = flag.Float64("tone", 50e3, "test tone in Hz from center")
		realtime = flag.Bool("realtime", true, "produce samples at 2.4 MS/s instead of as fast as possible")
		debug    = flag.Bool("d", false, "debug log")
	)
	flag.Parse()
	if err := gzip.SetLevel(stdgzip.BestSpeed); err != nil {
		log.Fatal("gzip: ", err)
	}
	var level slog.Level
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	dev := &fake.Device{Type: tuner.R828D, V4: true, Tone: *tone}
	if *realtime {
		dev.Pace = time.Second * remote.FrameBytes / 2 / 2_400_000
	}
	srv := remote.New(dev)
	// Same start state as rtl_remote's defaults.
	if _, err := srv.Configure(context.Background(), &pb.Settings{
		SampleRateHz: proto.Uint32(2_400_000),
		FrequencyHz:  proto.Uint64(100_000_000),
		GainManual:   proto.Bool(false),
	}); err != nil {
		log.Fatal("configure: ", err)
	}

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal("listen: ", err)
	}
	s := grpc.NewServer(
		grpc.InitialWindowSize(remote.Window),
		grpc.InitialConnWindowSize(remote.Window),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 5 * time.Second, Timeout: 5 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 2 * time.Second, PermitWithoutStream: true}),
	)
	pb.RegisterReceiverServer(s, srv)
	slog.Info("listening", "addr", l.Addr(), "tuner", dev.Chip(), "blog_v4", dev.BlogV4())
	if err := s.Serve(l); err != nil {
		log.Fatal("serve: ", err)
	}
}
