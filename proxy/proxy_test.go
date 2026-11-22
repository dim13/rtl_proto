package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dim13/r82xx/tuner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dim13/rtl_proto/remote"
	"github.com/dim13/rtl_proto/remote/fake"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// session runs a remote over bufconn and a proxy on a pipe, and
// returns the client end of the pipe with the greeting already read.
func session(t *testing.T, dev *fake.Device, opts Options) (net.Conn, <-chan error) {
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
	go func() {
		done <- New(pb.NewReceiverClient(cc), opts).Serve(ctx, server)
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
	eventually(t, func() bool { return dev.Snapshot().Closed == 1 })
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
