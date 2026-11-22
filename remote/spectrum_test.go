package remote

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/remote/fake"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// realtime paces the fake at 2.4 MS/s.
const realtime = time.Second * FrameBytes / 2 / 2_400_000

func openSpectrum(t *testing.T, c pb.ReceiverClient, req *pb.SpectrumRequest) grpc.ServerStreamingClient[pb.SpectrumLine] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := c.Spectrum(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func configure(t *testing.T, c pb.ReceiverClient, s *pb.Settings) {
	t.Helper()
	if _, err := c.Configure(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func dbfs(v byte) float64 {
	return float64(v)/2 - 127.5
}

func TestSpectrumTone(t *testing.T) {
	c := dial(t, &fake.Device{Tone: 300e3})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	st := openSpectrum(t, c, &pb.SpectrumRequest{Bins: proto.Uint32(1024)})
	line, err := st.Recv()
	if err != nil {
		t.Fatal(err)
	}
	p := line.GetPower()
	if len(p) != 1024 {
		t.Fatalf("bins: got %d, want 1024", len(p))
	}
	peak := 0
	for i, v := range p {
		if v > p[peak] {
			peak = i
		}
	}
	if peak < 639 || peak > 641 {
		t.Errorf("peak at bin %d, want 640±1", peak)
	}
	want := 20 * math.Log10(100/127.5)
	if got := dbfs(p[peak]); math.Abs(got-want) > 3 {
		t.Errorf("peak %.1f dBFS, want %.1f±3", got, want)
	}
	sorted := slices.Clone(p)
	slices.Sort(sorted)
	if d := dbfs(p[peak]) - dbfs(sorted[len(sorted)/2]); d < 40 {
		t.Errorf("peak only %.1f dB above median, want ≥ 40", d)
	}
}

func TestSpectrumDefaults(t *testing.T) {
	c := dial(t, &fake.Device{Pace: realtime})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	st := openSpectrum(t, c, &pb.SpectrumRequest{})
	start := time.Now()
	var n int
	for time.Since(start) < time.Second {
		line, err := st.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if len(line.GetPower()) != 2048 {
			t.Fatalf("bins: got %d, want 2048", len(line.GetPower()))
		}
		n++
	}
	if n < 15 || n > 35 {
		t.Errorf("%d lines in 1s, want ~25", n)
	}
}

func TestSpectrumInvalid(t *testing.T) {
	c := dial(t, &fake.Device{})
	for _, req := range []*pb.SpectrumRequest{
		{Bins: proto.Uint32(1000)},
		{Bins: proto.Uint32(32)},
		{Bins: proto.Uint32(1 << 17)},
		{LinesPerSecond: proto.Uint32(61)},
	} {
		_, err := openSpectrum(t, c, req).Recv()
		wantCode(t, err, codes.InvalidArgument)
	}
	if _, err := openSpectrum(t, c, &pb.SpectrumRequest{LinesPerSecond: proto.Uint32(0)}).Recv(); err != nil {
		t.Errorf("lines 0 (default): %v", err)
	}
}

func TestSpectrumRetune(t *testing.T) {
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	st := openSpectrum(t, c, &pb.SpectrumRequest{Bins: proto.Uint32(256)})
	first, err := st.Recv()
	if err != nil {
		t.Fatal(err)
	}
	configure(t, c, &pb.Settings{FrequencyHz: proto.Uint64(101e6)})
	for range 20 {
		line, err := st.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if line.GetCenterHz() == 101e6 && line.GetSettingsGen() > first.GetSettingsGen() {
			return
		}
	}
	t.Error("no line with the new center and generation")
}

func TestSpectrumLimit(t *testing.T) {
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	for range 8 {
		if _, err := openSpectrum(t, c, &pb.SpectrumRequest{Bins: proto.Uint32(64)}).Recv(); err != nil {
			t.Fatal(err)
		}
	}
	_, err := openSpectrum(t, c, &pb.SpectrumRequest{Bins: proto.Uint32(64)}).Recv()
	wantCode(t, err, codes.ResourceExhausted)
}
