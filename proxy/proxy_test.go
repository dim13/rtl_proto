package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dim13/r82xx/tuner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dim13/rtl_proto/remote"
	"github.com/dim13/rtl_proto/remote/fake"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// session runs a remote over bufconn and a proxy on a pipe, and
// returns the client end of the pipe with the greeting already read.
func session(t *testing.T, dev *fake.Device, opts Options) (net.Conn, <-chan error) {
	t.Helper()
	return sessionWith(t, dev, opts, nil)
}

// sessionWith lets wrap intercept the proxy's calls to the remote.
func sessionWith(t *testing.T, dev *fake.Device, opts Options, wrap func(pb.ReceiverClient) pb.ReceiverClient) (net.Conn, <-chan error) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(grpc.InitialWindowSize(remote.Window), grpc.InitialConnWindowSize(remote.Window))
	pb.RegisterReceiverServer(s, remote.New(dev))
	go s.Serve(lis)
	t.Cleanup(s.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(remote.Window),
		grpc.WithInitialConnWindowSize(remote.Window))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	rc := pb.NewReceiverClient(cc)
	if wrap != nil {
		rc = wrap(rc)
	}
	go func() {
		done <- New(rc, opts).Serve(ctx, server)
	}()
	return client, done
}

func readGreeting(t *testing.T, c net.Conn) []byte {
	t.Helper()
	hdr := make([]byte, 12)
	if _, err := io.ReadFull(c, hdr); err != nil {
		t.Fatal(err)
	}
	return hdr
}

func send(t *testing.T, c net.Conn, cmd byte, arg uint32) {
	t.Helper()
	b := []byte{cmd, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[1:], arg)
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

// drain keeps reading samples so the proxy never blocks on the pipe.
func drain(c net.Conn) {
	go io.Copy(io.Discard, c)
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

func TestGreeting(t *testing.T) {
	c, _ := session(t, &fake.Device{Type: tuner.R828D, V4: true, Pace: time.Millisecond}, Options{})
	hdr := readGreeting(t, c)
	if got := string(hdr[:4]); got != "RTL0" {
		t.Errorf("magic: got %q, want RTL0", got)
	}
	if got := binary.BigEndian.Uint32(hdr[4:]); got != 6 {
		t.Errorf("tuner: got %d, want 6", got)
	}
	if got := binary.BigEndian.Uint32(hdr[8:]); got != 29 {
		t.Errorf("gains: got %d, want 29", got)
	}
}

func TestSamplesExact(t *testing.T) {
	c, _ := session(t, &fake.Device{Pace: time.Millisecond}, Options{MinBits: 8, MaxBits: 8})
	readGreeting(t, c)
	buf := make([]byte, 3*remote.FrameBytes)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	for blk := 0; blk < len(buf); blk += remote.FrameBytes {
		for i := blk; i < blk+remote.FrameBytes-1; i++ {
			if buf[i+1] != buf[i]+1 {
				t.Fatalf("[%d]: %d then %d", i, buf[i], buf[i+1])
			}
		}
	}
}

func TestCommands(t *testing.T) {
	dev := &fake.Device{Pace: time.Millisecond}
	c, _ := session(t, dev, Options{})
	readGreeting(t, c)
	drain(c)
	send(t, c, 0x01, 100_000_000)
	send(t, c, 0x0d, 10)
	send(t, c, 0x0e, 1)
	send(t, c, 0x09, 2)
	eventually(t, func() bool {
		st := dev.Snapshot()
		return st.Freq == 100e6 && st.Gain == tuner.GainSteps[10] && st.BiasTee
	})
	send(t, c, 0x01, 101_000_000) // connection still open
	eventually(t, func() bool { return dev.Snapshot().Freq == 101e6 })
}

func TestUnknownCommand(t *testing.T) {
	dev := &fake.Device{Pace: time.Millisecond}
	c, _ := session(t, dev, Options{})
	readGreeting(t, c)
	drain(c)
	send(t, c, 0xff, 0)
	send(t, c, 0x01, 99_000_000)
	eventually(t, func() bool { return dev.Snapshot().Freq == 99e6 })
}

func TestClientDisconnect(t *testing.T) {
	dev := &fake.Device{Pace: time.Millisecond}
	c, done := session(t, dev, Options{})
	readGreeting(t, c)
	buf := make([]byte, remote.FrameBytes)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: got %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return")
	}
	eventually(t, func() bool { return dev.Snapshot().Released == 1 })
}

func TestReconnect(t *testing.T) {
	dev := &fake.Device{FailAfter: 50, Pace: time.Millisecond}
	c, done := session(t, dev, Options{Backoff: 10 * time.Millisecond})
	readGreeting(t, c)
	send(t, c, 0x01, 100_000_000)
	eventually(t, func() bool { return dev.Snapshot().Freq == 100e6 })
	// The first sample reader fails after 50 frames; samples must keep
	// coming from a new stream.
	buf := make([]byte, 100*remote.FrameBytes)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, call := range dev.Snapshot().Calls {
		if call == "SetFrequency" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("SetFrequency calls: got %d, want 2 (original + re-applied)", n)
	}
	select {
	case err := <-done:
		t.Fatalf("Serve returned: %v", err)
	default:
	}
}

// flaky fails the listed Configure calls (1-based) with Unavailable.
type flaky struct {
	pb.ReceiverClient
	n    atomic.Int32
	fail map[int32]bool
}

func (f *flaky) Configure(ctx context.Context, in *pb.Settings, opts ...grpc.CallOption) (*pb.Settings, error) {
	if f.fail[f.n.Add(1)] {
		return nil, status.Error(codes.Unavailable, "flaky")
	}
	return f.ReceiverClient.Configure(ctx, in, opts...)
}

func count(calls []string, name string) int {
	var n int
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

// readFrames reads n frames worth of samples, enough to span a reconnect.
func readFrames(t *testing.T, c net.Conn, n int) {
	t.Helper()
	if _, err := io.ReadFull(c, make([]byte, n*remote.FrameBytes)); err != nil {
		t.Fatal(err)
	}
}

func TestGainRounded(t *testing.T) {
	dev := &fake.Device{Pace: time.Millisecond}
	c, _ := session(t, dev, Options{})
	readGreeting(t, c)
	drain(c)
	send(t, c, 0x04, 200)
	eventually(t, func() bool { return dev.Snapshot().Gain == 197 })
}

// Re-applying after a reconnect must not turn auto gain into manual.
func TestReapplyKeepsAutoGain(t *testing.T) {
	dev := &fake.Device{FailAfter: 50, Pace: time.Millisecond}
	c, _ := session(t, dev, Options{Backoff: 10 * time.Millisecond})
	readGreeting(t, c)
	send(t, c, 0x03, 1)
	send(t, c, 0x04, 197)
	send(t, c, 0x03, 0)
	eventually(t, func() bool { return count(dev.Snapshot().Calls, "SetGainAuto") == 1 })
	readFrames(t, c, 100)
	if st := dev.Snapshot(); count(st.Calls, "SetGainAuto") != 2 || st.Manual {
		t.Errorf("after re-apply: manual %v, calls %v", st.Manual, st.Calls)
	}
}

// A re-apply that fails while the remote is still down is retried
// before streaming resumes.
func TestReapplyRetries(t *testing.T) {
	dev := &fake.Device{FailAfter: 50, Pace: time.Millisecond}
	c, _ := sessionWith(t, dev, Options{Backoff: 10 * time.Millisecond}, func(rc pb.ReceiverClient) pb.ReceiverClient {
		return &flaky{ReceiverClient: rc, fail: map[int32]bool{2: true}}
	})
	readGreeting(t, c)
	send(t, c, 0x01, 100_000_000)
	eventually(t, func() bool { return dev.Snapshot().Freq == 100e6 })
	readFrames(t, c, 100)
	if got := count(dev.Snapshot().Calls, "SetFrequency"); got != 2 {
		t.Errorf("SetFrequency calls: got %d, want 2", got)
	}
}

// A command that fails because the remote is unreachable is still what
// the client wants: it is applied after the reconnect.
func TestCommandDuringOutage(t *testing.T) {
	dev := &fake.Device{FailAfter: 50, Pace: time.Millisecond}
	c, _ := sessionWith(t, dev, Options{Backoff: 10 * time.Millisecond}, func(rc pb.ReceiverClient) pb.ReceiverClient {
		return &flaky{ReceiverClient: rc, fail: map[int32]bool{2: true}}
	})
	readGreeting(t, c)
	send(t, c, 0x01, 100_000_000)
	send(t, c, 0x01, 101_000_000)
	readFrames(t, c, 100)
	if got := dev.Snapshot().Freq; got != 101e6 {
		t.Errorf("freq after reconnect: got %v, want 101e6", got)
	}
}
