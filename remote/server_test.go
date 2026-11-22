package remote

import (
	"context"
	"math"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/dim13/r82xx/tuner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/codec"
	"github.com/dim13/rtl_proto/grpczstd"
	"github.com/dim13/rtl_proto/remote/fake"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

func dial(t *testing.T, dev *fake.Device) pb.ReceiverClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(grpc.InitialWindowSize(Window), grpc.InitialConnWindowSize(Window))
	pb.RegisterReceiverServer(s, New(dev))
	go s.Serve(lis)
	t.Cleanup(s.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(Window),
		grpc.WithInitialConnWindowSize(Window))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return pb.NewReceiverClient(cc)
}

func openStream(t *testing.T, c pb.ReceiverClient, req *pb.StreamRequest) grpc.ServerStreamingClient[pb.Frame] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := c.Stream(ctx, req, grpc.UseCompressor(grpczstd.Name))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func recv(t *testing.T, st grpc.ServerStreamingClient[pb.Frame]) *pb.Frame {
	t.Helper()
	f, err := st.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// checkFrame decodes f and compares it with the fake's counter pattern.
func checkFrame(t *testing.T, f *pb.Frame) {
	t.Helper()
	out := make([]byte, 2*f.GetCount())
	if err := codec.Decode(out, f.GetData(), int(f.GetBits()), f.GetStep()); err != nil {
		t.Fatal(err)
	}
	limit := float64(f.GetStep())/2 + 0.5
	for i, b := range out {
		want := byte(f.GetFirstSample()*2 + uint64(i))
		if f.GetBits() == 8 && b != want || math.Abs(float64(b)-float64(want)) > limit {
			t.Fatalf("frame %d bits %d [%d]: got %d, want %d", f.GetFirstSample(), f.GetBits(), i, b, want)
		}
	}
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("got %v (%v), want %v", got, err, want)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 1s")
}

func TestInfo(t *testing.T) {
	c := dial(t, &fake.Device{Type: tuner.R828D, V4: true})
	info, err := c.Info(context.Background(), &pb.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.GetTuner(), pb.TunerType_TUNER_TYPE_R828D; got != want {
		t.Errorf("tuner: got %v, want %v", got, want)
	}
	if !info.GetBlogV4() {
		t.Error("blog_v4: got false, want true")
	}
	if got, want := len(info.GetGainsTenthDb()), 29; got != want {
		t.Errorf("gains: got %d, want %d", got, want)
	}
}

func TestConfigurePartial(t *testing.T) {
	dev := &fake.Device{}
	c := dial(t, dev)
	got, err := c.Configure(context.Background(), &pb.Settings{FrequencyHz: proto.Uint64(100e6)})
	if err != nil {
		t.Fatal(err)
	}
	st := dev.Snapshot()
	if st.Freq != 100e6 {
		t.Errorf("freq: got %v, want 100e6", st.Freq)
	}
	if want := []string{"SetFrequency"}; !slices.Equal(st.Calls, want) {
		t.Errorf("calls: got %v, want %v", st.Calls, want)
	}
	if got.FrequencyHz == nil || got.SampleRateHz == nil || got.SampleRateExactHz == nil ||
		got.Ppm == nil || got.GainManual == nil || got.GainTenthDb == nil ||
		got.Agc == nil || got.BiasTee == nil || got.DirectSampling == nil {
		t.Errorf("reply not fully set: %v", got)
	}
}

func TestConfigureOrder(t *testing.T) {
	dev := &fake.Device{}
	c := dial(t, dev)
	_, err := c.Configure(context.Background(), &pb.Settings{
		FrequencyHz:    proto.Uint64(100e6),
		SampleRateHz:   proto.Uint32(2_048_000),
		Ppm:            proto.Int32(3),
		GainManual:     proto.Bool(true),
		GainTenthDb:    proto.Int32(int32(tuner.GainSteps[10])),
		Agc:            proto.Bool(true),
		BiasTee:        proto.Bool(true),
		DirectSampling: pb.DirectSampling_DIRECT_SAMPLING_OFF.Enum(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SetPPM", "SetSampleRate", "SetBandwidth", "SetFrequency",
		"SetGainManual", "SetGain", "SetAGC", "SetBiasTee"}
	if got := dev.Snapshot().Calls; !slices.Equal(got, want) {
		t.Errorf("calls: got %v, want %v", got, want)
	}
}

func TestConfigureGainNotInTable(t *testing.T) {
	dev := &fake.Device{}
	c := dial(t, dev)
	_, err := c.Configure(context.Background(), &pb.Settings{GainTenthDb: proto.Int32(123)})
	wantCode(t, err, codes.InvalidArgument)
	if got := dev.Snapshot().Calls; len(got) != 0 {
		t.Errorf("calls: got %v, want none", got)
	}
}

func TestConfigureDirectSampling(t *testing.T) {
	q := &pb.Settings{DirectSampling: pb.DirectSampling_DIRECT_SAMPLING_Q.Enum()}
	_, err := dial(t, &fake.Device{}).Configure(context.Background(), q)
	wantCode(t, err, codes.Unimplemented)

	dev := &fake.Device{V4: true}
	got, err := dial(t, dev).Configure(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDirectSampling() != pb.DirectSampling_DIRECT_SAMPLING_Q {
		t.Errorf("direct sampling: got %v, want Q", got.GetDirectSampling())
	}
	if calls := dev.Snapshot().Calls; len(calls) != 0 {
		t.Errorf("calls: got %v, want none", calls)
	}
}

func TestConfigureDeviceError(t *testing.T) {
	c := dial(t, &fake.Device{FailRate: true, Pace: time.Millisecond})
	st := openStream(t, c, &pb.StreamRequest{})
	recv(t, st)
	_, err := c.Configure(context.Background(), &pb.Settings{SampleRateHz: proto.Uint32(1)})
	wantCode(t, err, codes.Internal)
	for range 3 {
		recv(t, st)
	}
}

func TestStreamLosslessExact(t *testing.T) {
	c := dial(t, &fake.Device{})
	st := openStream(t, c, &pb.StreamRequest{MinBits: proto.Uint32(8), MaxBits: proto.Uint32(8)})
	var last uint64
	for i := range 20 {
		f := recv(t, st)
		if f.GetBits() != 8 || f.GetCount() != 8192 || f.GetCapturedUnixNano() == 0 {
			t.Fatalf("frame %d: bits %d count %d captured %d", i, f.GetBits(), f.GetCount(), f.GetCapturedUnixNano())
		}
		if first := f.GetFirstSample(); first%8192 != 0 || i > 0 && first <= last {
			t.Fatalf("frame %d: first_sample %d after %d", i, first, last)
		}
		last = f.GetFirstSample()
		checkFrame(t, f)
	}
}

func TestStreamAdapts(t *testing.T) {
	// The device produces a frame per ms, the client takes one per 5 ms:
	// the link is saturated.
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	// No compression: the counter pattern would shrink to nothing.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := c.Stream(ctx, &pb.StreamRequest{MinBits: proto.Uint32(2)})
	if err != nil {
		t.Fatal(err)
	}
	var adapted bool
	for range 200 {
		time.Sleep(5 * time.Millisecond)
		f := recv(t, st)
		checkFrame(t, f)
		if f.GetBits() < 8 {
			adapted = true
			break
		}
	}
	if !adapted {
		t.Error("bits never dropped below 8")
	}
}

func TestStreamBitsInvalid(t *testing.T) {
	c := dial(t, &fake.Device{})
	for _, req := range []*pb.StreamRequest{
		{MinBits: proto.Uint32(9)},
		{MinBits: proto.Uint32(6), MaxBits: proto.Uint32(4)},
	} {
		_, err := openStream(t, c, req).Recv()
		wantCode(t, err, codes.InvalidArgument)
	}
}

func TestStreamTakeover(t *testing.T) {
	dev := &fake.Device{Pace: time.Millisecond}
	c := dial(t, dev)
	a := openStream(t, c, &pb.StreamRequest{})
	recv(t, a)
	b := openStream(t, c, &pb.StreamRequest{})
	recv(t, b)
	var err error
	for err == nil {
		_, err = a.Recv()
	}
	wantCode(t, err, codes.Aborted)
	for range 3 {
		recv(t, b)
	}
	eventually(t, func() bool { return dev.Snapshot().Released == 1 })
}

func TestStreamRetune(t *testing.T) {
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	st := openStream(t, c, &pb.StreamRequest{})
	before := recv(t, st).GetSettingsGen()
	if _, err := c.Configure(context.Background(), &pb.Settings{FrequencyHz: proto.Uint64(101e6)}); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if recv(t, st).GetSettingsGen() > before {
			return
		}
	}
	t.Error("settings_gen did not increase after retune")
}

// A stream whose client stopped reading (Send blocked on flow control)
// must not keep a new stream from taking over.
func TestTakeoverUnreadStream(t *testing.T) {
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	// Uncompressed, so the pinned window fills and a's Send blocks.
	actx, acancel := context.WithCancel(context.Background())
	defer acancel()
	a, err := c.Stream(actx, &pb.StreamRequest{MinBits: proto.Uint32(8)})
	if err != nil {
		t.Fatal(err)
	}
	recv(t, a)
	time.Sleep(100 * time.Millisecond)
	b := openStream(t, c, &pb.StreamRequest{})
	got := make(chan error, 1)
	go func() {
		_, err := b.Recv()
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("new stream got no frame within 1s")
	}
}
