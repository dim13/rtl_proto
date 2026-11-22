package remote

import (
	"context"
	"encoding/binary"
	"math"
	"math/cmplx"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/remote/fake"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

type channel = grpc.BidiStreamingClient[pb.ChannelRequest, pb.ChannelFrame]

func openChannel(t *testing.T, c pb.ReceiverClient, req *pb.ChannelRequest) (channel, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := c.Channel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(req); err != nil {
		t.Fatal(err)
	}
	return ch, cancel
}

func iq(f *pb.ChannelFrame) []complex128 {
	b := f.GetIq()
	out := make([]complex128, len(b)/4)
	for i := range out {
		re := int16(binary.LittleEndian.Uint16(b[4*i:]))
		im := int16(binary.LittleEndian.Uint16(b[4*i+2:]))
		out[i] = complex(float64(re), float64(im))
	}
	return out
}

// toneHz estimates the frequency of a complex tone from the mean phase step.
func toneHz(x []complex128, rate float64) float64 {
	var sum float64
	for i := 1; i < len(x); i++ {
		sum += cmplx.Phase(x[i] * cmplx.Conj(x[i-1]))
	}
	return sum / float64(len(x)-1) / (2 * math.Pi) * rate
}

func meanAbs(x []complex128) float64 {
	var s float64
	for _, v := range x {
		s += cmplx.Abs(v)
	}
	return s / float64(len(x))
}

func recvFrame(t *testing.T, ch channel) *pb.ChannelFrame {
	t.Helper()
	f, err := ch.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func req(offset int64, rate uint32) *pb.ChannelRequest {
	return &pb.ChannelRequest{OffsetHz: proto.Int64(offset), SampleRateHz: proto.Uint32(rate)}
}

func TestChannelTone(t *testing.T) {
	c := dial(t, &fake.Device{Tone: 100e3})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	ch, _ := openChannel(t, c, req(100e3, 48_000))
	recvFrame(t, ch) // filter warm-up
	recvFrame(t, ch)
	f := recvFrame(t, ch)
	x := iq(f)
	if hz := toneHz(x, 48_000); math.Abs(hz) > 50 {
		t.Errorf("output tone at %.0f Hz, want DC", hz)
	}
	want := 100 / 127.5 * 32767
	if db := 20 * math.Log10(meanAbs(x)/want); math.Abs(db) > 1 {
		t.Errorf("magnitude %.2f dB off", db)
	}
	if f.GetSampleRateHz() != 48_000 || f.GetOffsetHz() != 100e3 {
		t.Errorf("frame rate %d offset %d", f.GetSampleRateHz(), f.GetOffsetHz())
	}
}

func TestChannelOffsetUpdate(t *testing.T) {
	c := dial(t, &fake.Device{Tone: 100e3, Pace: time.Millisecond})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	ch, _ := openChannel(t, c, req(90e3, 48_000))
	recvFrame(t, ch)
	recvFrame(t, ch)
	if hz := toneHz(iq(recvFrame(t, ch)), 48_000); math.Abs(hz-10e3) > 100 {
		t.Fatalf("before update: tone at %.0f Hz, want 10000", hz)
	}
	if err := ch.Send(&pb.ChannelRequest{OffsetHz: proto.Int64(100e3)}); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		f := recvFrame(t, ch)
		if f.GetOffsetHz() != 100e3 {
			continue
		}
		recvFrame(t, ch) // let the filter settle on the new offset
		if hz := toneHz(iq(recvFrame(t, ch)), 48_000); math.Abs(hz) > 100 {
			t.Errorf("after update: tone at %.0f Hz, want DC", hz)
		}
		return
	}
	t.Error("offset update never took effect")
}

func TestChannelInvalid(t *testing.T) {
	c := dial(t, &fake.Device{})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	for _, r := range []*pb.ChannelRequest{
		req(0, 44_100),
		req(1_300_000, 48_000),
		{OffsetHz: proto.Int64(0), SampleRateHz: proto.Uint32(48_000), BandwidthHz: proto.Uint32(60_000)},
		{OffsetHz: proto.Int64(0)},
	} {
		ch, _ := openChannel(t, c, r)
		_, err := ch.Recv()
		wantCode(t, err, codes.InvalidArgument)
	}
}

func TestChannelRateChange(t *testing.T) {
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	ch, _ := openChannel(t, c, req(0, 48_000))
	recvFrame(t, ch)
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(1_920_000)})
	for range 5 {
		if f := recvFrame(t, ch); f.GetSampleRateHz() != 48_000 {
			t.Fatalf("rate %d after compatible change", f.GetSampleRateHz())
		}
	}
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_048_000)})
	for range 50 {
		if _, err := ch.Recv(); err != nil {
			wantCode(t, err, codes.FailedPrecondition)
			return
		}
	}
	t.Error("stream kept running at an incompatible rate")
}

func TestChannelLimit(t *testing.T) {
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	var cancels []context.CancelFunc
	for range 8 {
		ch, cancel := openChannel(t, c, req(0, 48_000))
		recvFrame(t, ch)
		cancels = append(cancels, cancel)
	}
	ch, _ := openChannel(t, c, req(0, 48_000))
	_, err := ch.Recv()
	wantCode(t, err, codes.ResourceExhausted)
	cancels[0]()
	eventually(t, func() bool {
		ch, cancel := openChannel(t, c, req(0, 48_000))
		defer cancel()
		_, err := ch.Recv()
		return err == nil
	})
}

func TestChannelWithStreamAndSpectrum(t *testing.T) {
	c := dial(t, &fake.Device{Pace: time.Millisecond})
	st := openStream(t, c, &pb.StreamRequest{})
	sp := openSpectrum(t, c, &pb.SpectrumRequest{Bins: proto.Uint32(256)})
	ch, _ := openChannel(t, c, req(0, 240_000))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		recv(t, st)
		if _, err := sp.Recv(); err != nil {
			t.Fatal(err)
		}
		recvFrame(t, ch)
	}
}

// Frames the channel had to drop show up as gaps in first_sample.
func TestChannelReportsDrops(t *testing.T) {
	c := dial(t, &fake.Device{}) // unpaced: produces faster than the channel can filter
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	ch, _ := openChannel(t, c, req(0, 48_000))
	next := uint64(0)
	for range 400 {
		f := recvFrame(t, ch)
		if f.GetFirstSample() > next {
			return
		}
		next = f.GetFirstSample() + uint64(f.GetCount())
	}
	t.Error("no gap reported although the device outruns the channel")
}

// A slow Configure (e.g. gain over I²C) must not stall channel frames.
func TestChannelDuringSlowConfigure(t *testing.T) {
	c := dial(t, &fake.Device{SlowGain: 500 * time.Millisecond, Pace: realtime})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	ch, _ := openChannel(t, c, req(0, 48_000))
	recvFrame(t, ch)
	go c.Configure(context.Background(), &pb.Settings{GainTenthDb: proto.Int32(197)})
	time.Sleep(50 * time.Millisecond) // let Configure take the lock
	last := time.Now()
	for range 50 {
		recvFrame(t, ch)
		if d := time.Since(last); d > 100*time.Millisecond {
			t.Fatalf("frame gap %v during Configure", d)
		}
		last = time.Now()
	}
}

// Requests that would need a huge or very busy filter are refused.
func TestChannelFilterCost(t *testing.T) {
	c := dial(t, &fake.Device{})
	configure(t, c, &pb.Settings{SampleRateHz: proto.Uint32(2_400_000)})
	for _, r := range []*pb.ChannelRequest{
		req(0, 1),
		req(0, 100),
		{OffsetHz: proto.Int64(0), SampleRateHz: proto.Uint32(2_400_000), BandwidthHz: proto.Uint32(2_400_000)},
	} {
		ch, _ := openChannel(t, c, r)
		done := make(chan error, 1)
		go func() {
			_, err := ch.Recv()
			done <- err
		}()
		select {
		case err := <-done:
			wantCode(t, err, codes.InvalidArgument)
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("rate %d: no answer within 200ms", r.GetSampleRateHz())
		}
	}
}
