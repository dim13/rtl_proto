package remote

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/dsp"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// chanConfig is what a client asked for, validated against a device rate.
type chanConfig struct {
	offset int64
	rate   uint32
	bw     uint32
}

// pipe shifts, filters and decimates one channel.
type pipe struct {
	devRate uint32
	nco     *dsp.NCO
	dec     *dsp.Decimator
	in, out []complex64
}

// merge applies the fields set in r.
func (c chanConfig) merge(r *pb.ChannelRequest) chanConfig {
	if r.OffsetHz != nil {
		c.offset = r.GetOffsetHz()
	}
	if r.SampleRateHz != nil {
		c.rate = r.GetSampleRateHz()
		c.bw = 0
	}
	if r.BandwidthHz != nil {
		c.bw = r.GetBandwidthHz()
	}
	if c.bw == 0 {
		c.bw = c.rate * 4 / 5
	}
	return c
}

func (c chanConfig) check(devRate uint32) error {
	switch {
	case c.rate == 0 || devRate%c.rate != 0:
		return fmt.Errorf("rate %d does not divide device rate %d", c.rate, devRate)
	case c.offset < -int64(devRate)/2 || c.offset > int64(devRate)/2:
		return fmt.Errorf("offset %d outside ±%d", c.offset, devRate/2)
	case c.bw > c.rate:
		return fmt.Errorf("bandwidth %d above rate %d", c.bw, c.rate)
	}
	return nil
}

func newPipe(c chanConfig, devRate uint32) *pipe {
	p := &pipe{
		devRate: devRate,
		nco:     dsp.NewNCO(-float64(c.offset) / float64(devRate)),
	}
	p.filter(c)
	return p
}

// filter builds the decimator; the NCO keeps its phase.
func (p *pipe) filter(c chanConfig) {
	fs := float64(p.devRate)
	pass := float64(c.bw) / 2
	stop := max(float64(c.rate)-pass, pass+0.05*float64(c.rate))
	p.dec = dsp.NewDecimator(dsp.Lowpass(pass/fs, stop/fs), int(p.devRate/c.rate))
}

// process turns one raw u8 frame into int16 I/Q bytes.
func (p *pipe) process(raw []byte) (iq []byte, count int) {
	p.in = p.in[:0]
	for i := 0; i < len(raw); i += 2 {
		p.in = append(p.in, complex((float32(raw[i])-127.5)/127.5, (float32(raw[i+1])-127.5)/127.5))
	}
	p.nco.Mix(p.in)
	p.out = p.dec.Process(p.out, p.in)
	iq = make([]byte, 4*len(p.out))
	for i, v := range p.out {
		binary.LittleEndian.PutUint16(iq[4*i:], uint16(toInt16(real(v))))
		binary.LittleEndian.PutUint16(iq[4*i+2:], uint16(toInt16(imag(v))))
	}
	return iq, len(p.out)
}

func toInt16(v float32) int16 {
	return int16(max(-32768, min(32767, math.Round(float64(v)*32767))))
}

func (s *Server) Channel(ss grpc.BidiStreamingServer[pb.ChannelRequest, pb.ChannelFrame]) error {
	ctx := ss.Context()
	r, err := ss.Recv()
	if err != nil {
		return err
	}
	_, devRate := s.tuning()
	cfg := chanConfig{}.merge(r)
	if err := cfg.check(devRate); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if !s.channels.acquire() {
		return status.Error(codes.ResourceExhausted, "too many channel streams")
	}
	defer s.channels.release()

	updates := make(chan *pb.ChannelRequest, 4)
	go func() {
		for {
			r, err := ss.Recv()
			if err != nil {
				return // client done sending, or stream over
			}
			select {
			case updates <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	q, unsub := s.hub.subscribe(queueFrames)
	defer unsub()

	p := newPipe(cfg, devRate)
	var first uint64
	for {
		f, err := q.get(ctx)
		if err != nil {
			if ctx.Err() == nil {
				return status.Error(codes.Internal, err.Error())
			}
			return status.FromContextError(ctx.Err()).Err()
		}
		if cfg, err = s.update(ctx, cfg, p, updates); err != nil {
			return err
		}
		iq, n := p.process(f.raw)
		err = ss.Send(&pb.ChannelFrame{
			FirstSample:      proto.Uint64(first),
			Count:            proto.Uint32(uint32(n)),
			SampleRateHz:     proto.Uint32(cfg.rate),
			OffsetHz:         proto.Int64(cfg.offset),
			SettingsGen:      proto.Uint64(s.gen.Load()),
			CapturedUnixNano: proto.Int64(f.captured.UnixNano()),
			Iq:               iq,
		})
		if err != nil {
			return err
		}
		first += uint64(n)
	}
}

// update applies client changes and device rate changes before a frame.
func (s *Server) update(ctx context.Context, cfg chanConfig, p *pipe, updates <-chan *pb.ChannelRequest) (chanConfig, error) {
	if _, devRate := s.tuning(); devRate != p.devRate {
		if err := cfg.check(devRate); err != nil {
			return cfg, status.Error(codes.FailedPrecondition, err.Error())
		}
		p.devRate = devRate
		p.nco.SetFreq(-float64(cfg.offset) / float64(devRate))
		p.filter(cfg)
	}
	for {
		select {
		case r := <-updates:
			next := cfg.merge(r)
			if err := next.check(p.devRate); err != nil {
				return cfg, status.Error(codes.InvalidArgument, err.Error())
			}
			if next.offset != cfg.offset {
				p.nco.SetFreq(-float64(next.offset) / float64(p.devRate))
			}
			if next.rate != cfg.rate || next.bw != cfg.bw {
				p.filter(next)
			}
			cfg = next
		default:
			return cfg, nil
		}
	}
}
