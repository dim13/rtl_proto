// Command rtl_remote serves an RTL-SDR over the rtlproto.v1 gRPC
// protocol.
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

	"github.com/dim13/r82xx"
	"github.com/dim13/r82xx/tuner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/remote"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// listener keeps the kernel send buffer small, so a saturated link
// blocks Send instead of queueing samples out of the controller's
// sight.
type listener struct {
	net.Listener
}

func (l listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetWriteBuffer(remote.Window)
	}
	return c, err
}

// nearestGain maps dB to the closest gain table entry, in tenths of dB.
func nearestGain(db float64) int32 {
	best := tuner.GainSteps[0]
	for _, g := range tuner.GainSteps {
		if abs(float64(g)-db*10) < abs(float64(best)-db*10) {
			best = g
		}
	}
	return int32(best)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func main() {
	var (
		addr  = flag.String("a", ":7373", "listen address")
		freq  = flag.Float64("f", 100e6, "frequency in Hz")
		rate  = flag.Uint("s", 2_048_000, "sample rate in Hz")
		gain  = flag.Float64("g", 0, "gain in dB, 0 for auto")
		ppm   = flag.Int("P", 0, "frequency correction in ppm")
		debug = flag.Bool("d", false, "debug log")
	)
	flag.Parse()
	// Speed over ratio: per-frame ratios match higher levels on I/Q.
	if err := gzip.SetLevel(stdgzip.BestSpeed); err != nil {
		log.Fatal("gzip: ", err)
	}

	var level slog.Level
	if *debug {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
	// The log package now writes through slog; only log.Fatal uses it.
	slog.SetLogLoggerLevel(slog.LevelError)

	u, err := openUSB()
	if err != nil {
		log.Fatal("usb: ", err)
	}
	defer u.Close()
	dev, err := r82xx.Open(u)
	if err != nil {
		log.Fatal("open: ", err)
	}
	defer dev.Close()

	srv := remote.New(device{Device: dev, u: u})
	initial := &pb.Settings{
		Ppm:          proto.Int32(int32(*ppm)),
		SampleRateHz: proto.Uint32(uint32(*rate)),
		FrequencyHz:  proto.Uint64(uint64(*freq)),
		GainManual:   proto.Bool(*gain != 0),
	}
	if *gain != 0 {
		initial.GainTenthDb = proto.Int32(nearestGain(*gain))
	}
	if _, err := srv.Configure(context.Background(), initial); err != nil {
		log.Fatal("configure: ", err)
	}

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal("listen: ", err)
	}
	s := grpc.NewServer(
		grpc.InitialWindowSize(remote.Window),
		grpc.InitialConnWindowSize(remote.Window),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    5 * time.Second,
			Timeout: 5 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             2 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	pb.RegisterReceiverServer(s, srv)
	slog.Info("listening", "addr", l.Addr(), "tuner", dev.Chip(), "blog_v4", dev.BlogV4())
	if err := s.Serve(listener{l}); err != nil {
		log.Fatal("serve: ", err)
	}
}
