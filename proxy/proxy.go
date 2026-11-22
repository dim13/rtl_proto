// Package proxy serves rtl_tcp clients from a remote rtlproto.v1
// Receiver.
package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/codec"
	"github.com/dim13/rtl_proto/grpczstd"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

var commandNames = [...]string{
	cmdFrequency:      "frequency",
	cmdSampleRate:     "sample rate",
	cmdGainMode:       "gain mode",
	cmdGain:           "gain",
	cmdFreqCorrection: "freq correction",
	cmdIFGain:         "if gain",
	cmdTestMode:       "test mode",
	cmdAGCMode:        "agc mode",
	cmdDirectSampling: "direct sampling",
	cmdOffsetTuning:   "offset tuning",
	cmdRTLXtal:        "rtl xtal",
	cmdTunerXtal:      "tuner xtal",
	cmdGainByIndex:    "gain by index",
	cmdBiasTee:        "bias tee",
}

type command byte

// rtl_tcp commands.
const (
	_                 command = iota
	cmdFrequency              // Hz
	cmdSampleRate             // Hz
	cmdGainMode               // 0 auto, 1 manual
	cmdGain                   // tenths of dB
	cmdFreqCorrection         // ppm
	cmdIFGain                 // stage<<16 | tenths of dB
	cmdTestMode               // 0/1
	cmdAGCMode                // 0/1
	cmdDirectSampling         // 0 off, 1 I, 2 Q
	cmdOffsetTuning           // 0/1
	cmdRTLXtal                // Hz
	cmdTunerXtal              // Hz
	cmdGainByIndex            // index into gain table
	cmdBiasTee                // 0/1
)

// Options tune a Proxy.
type Options struct {
	MinBits, MaxBits uint32        // passed to the remote, 0 for its defaults
	Backoff          time.Duration // wait before reconnecting, 0 for 500 ms
}

// Proxy serves one rtl_tcp client at a time from a remote Receiver.
type Proxy struct {
	c    pb.ReceiverClient
	opts Options

	mu   sync.Mutex
	last *pb.Settings // merged settings sent successfully
}

// stats summarizes the stream once per second.
type stats struct {
	next    uint64 // expected first_sample
	dropped uint64
	bits    uint32
	latency time.Duration
	since   time.Time
}

func (c command) String() string {
	if int(c) < len(commandNames) && commandNames[c] != "" {
		return commandNames[c]
	}
	return fmt.Sprintf("%#x", byte(c))
}

func New(c pb.ReceiverClient, opts Options) *Proxy {
	if opts.Backoff == 0 {
		opts.Backoff = 500 * time.Millisecond
	}
	return &Proxy{
		c:    c,
		opts: opts,
		last: &pb.Settings{},
	}
}

// Serve handles one rtl_tcp client until it disconnects or ctx ends,
// and closes conn. A client disconnect is not an error.
func (p *Proxy) Serve(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	info, err := p.c.Info(ctx, &pb.InfoRequest{})
	if err != nil {
		return err
	}
	hdr := make([]byte, 12)
	copy(hdr, "RTL0")
	binary.BigEndian.PutUint32(hdr[4:], uint32(info.GetTuner()))
	binary.BigEndian.PutUint32(hdr[8:], uint32(len(info.GetGainsTenthDb())))
	if _, err := conn.Write(hdr); err != nil {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() {
		p.commands(ctx, conn, info.GetGainsTenthDb())
		cancel()
	})
	err = p.stream(ctx, conn)
	cancel()
	conn.Close()
	wg.Wait()
	return err
}

// commands reads rtl_tcp commands until the client goes away.
func (p *Proxy) commands(ctx context.Context, conn net.Conn, gains []int32) {
	cmd := make([]byte, 5)
	for {
		if _, err := io.ReadFull(conn, cmd); err != nil {
			return
		}
		c, arg := command(cmd[0]), binary.BigEndian.Uint32(cmd[1:])
		slog.Debug("command", "cmd", c, "arg", arg)
		s, err := settings(c, arg, gains)
		if err != nil {
			slog.Warn("command", "cmd", c, "arg", arg, "err", err)
			continue
		}
		if s == nil {
			continue
		}
		if _, err := p.c.Configure(ctx, s); err != nil {
			slog.Warn("command", "cmd", c, "arg", arg, "err", err)
			continue
		}
		p.mu.Lock()
		proto.Merge(p.last, s)
		p.mu.Unlock()
	}
}

// settings maps one rtl_tcp command; nil means nothing to send.
func settings(c command, arg uint32, gains []int32) (*pb.Settings, error) {
	switch c {
	case cmdFrequency:
		return &pb.Settings{FrequencyHz: proto.Uint64(uint64(arg))}, nil
	case cmdSampleRate:
		return &pb.Settings{SampleRateHz: proto.Uint32(arg)}, nil
	case cmdGainMode:
		return &pb.Settings{GainManual: proto.Bool(arg != 0)}, nil
	case cmdGain:
		return &pb.Settings{GainTenthDb: proto.Int32(int32(arg))}, nil
	case cmdFreqCorrection:
		return &pb.Settings{Ppm: proto.Int32(int32(arg))}, nil
	case cmdAGCMode:
		return &pb.Settings{Agc: proto.Bool(arg != 0)}, nil
	case cmdDirectSampling:
		return &pb.Settings{DirectSampling: pb.DirectSampling(arg).Enum()}, nil
	case cmdGainByIndex:
		if int(arg) >= len(gains) {
			return nil, fmt.Errorf("gain index %d out of range", arg)
		}
		return &pb.Settings{GainTenthDb: proto.Int32(gains[arg])}, nil
	case cmdBiasTee:
		return &pb.Settings{BiasTee: proto.Bool(arg != 0)}, nil
	case cmdTestMode, cmdOffsetTuning:
		// Off is the power-on state; clients send it on connect.
		if arg == 0 {
			return nil, nil
		}
		return nil, errors.ErrUnsupported
	default:
		return nil, errors.ErrUnsupported
	}
}

// stream copies decoded frames to conn until ctx ends or the client is
// gone.
func (p *Proxy) stream(ctx context.Context, conn net.Conn) error {
	st, err := p.c.Stream(ctx, &pb.StreamRequest{
		MinBits: proto.Uint32(p.opts.MinBits),
		MaxBits: proto.Uint32(p.opts.MaxBits),
	}, grpc.UseCompressor(grpczstd.Name))
	if err != nil {
		return streamErr(ctx, err)
	}
	var buf []byte
	s := stats{since: time.Now()}
	for {
		f, err := st.Recv()
		if err != nil {
			return streamErr(ctx, err)
		}
		n := 2 * int(f.GetCount())
		if cap(buf) < n {
			buf = make([]byte, n)
		}
		buf = buf[:n]
		if err := codec.Decode(buf, f.GetData(), int(f.GetBits()), f.GetStep()); err != nil {
			return err
		}
		if _, err := conn.Write(buf); err != nil {
			return nil // client gone
		}
		s.update(f)
	}
}

func (s *stats) update(f *pb.Frame) {
	if first := f.GetFirstSample(); first > s.next {
		s.dropped += first - s.next
	}
	s.next = f.GetFirstSample() + uint64(f.GetCount())
	s.bits = f.GetBits()
	s.latency = time.Since(time.Unix(0, f.GetCapturedUnixNano()))
	if time.Since(s.since) < time.Second {
		return
	}
	if s.dropped > 0 {
		slog.Warn("samples dropped", "samples", s.dropped, "bits", s.bits)
	}
	slog.Debug("stream", "bits", s.bits, "latency", s.latency.Round(time.Millisecond))
	s.dropped, s.since = 0, time.Now()
}

// streamErr hides errors caused by the session ending.
func streamErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}
