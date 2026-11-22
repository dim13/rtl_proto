package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dim13/r82xx/tuner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/codec"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

var (
	errSuperseded = errors.New("superseded")
	errSamples    = errors.New("samples")
)

// Window is the HTTP/2 flow-control window, pinned on both ends so
// that a saturated link blocks Send quickly instead of buffering out
// of the controller's sight. Setting it also turns off BDP probing.
const Window = 64 << 10

const (
	queueFrames = 4
	holdUp      = 2 * time.Second
)

// Server implements the Receiver service for one device.
type Server struct {
	pb.UnimplementedReceiverServer
	dev Device
	gen atomic.Uint64 // bumped by tuning changes

	mu sync.Mutex // serializes Configure, guards st
	st *pb.Settings

	smu    sync.Mutex // guards active
	active *session
}

// session is one running Stream. done closes once its sample reader
// has released the device.
type session struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func New(dev Device) *Server {
	return &Server{
		dev: dev,
		st: &pb.Settings{
			FrequencyHz:       proto.Uint64(0),
			SampleRateHz:      proto.Uint32(0),
			SampleRateExactHz: proto.Float64(0),
			Ppm:               proto.Int32(0),
			GainManual:        proto.Bool(false),
			GainTenthDb:       proto.Int32(0),
			Agc:               proto.Bool(false),
			BiasTee:           proto.Bool(false),
			DirectSampling:    pb.DirectSampling_DIRECT_SAMPLING_OFF.Enum(),
		},
	}
}

func (s *Server) Info(context.Context, *pb.InfoRequest) (*pb.DeviceInfo, error) {
	manufacturer, product, err := s.dev.Strings()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	var typ pb.TunerType
	switch s.dev.Chip() {
	case tuner.R820T:
		typ = pb.TunerType_TUNER_TYPE_R820T
	case tuner.R828D:
		typ = pb.TunerType_TUNER_TYPE_R828D
	}
	gains := make([]int32, len(tuner.GainSteps))
	for i, g := range tuner.GainSteps {
		gains[i] = int32(g)
	}
	s.mu.Lock()
	cur := proto.CloneOf(s.st)
	s.mu.Unlock()
	return &pb.DeviceInfo{
		Tuner:        typ.Enum(),
		BlogV4:       proto.Bool(s.dev.BlogV4()),
		GainsTenthDb: gains,
		Manufacturer: proto.String(manufacturer),
		Product:      proto.String(product),
		Current:      cur,
	}, nil
}

// Configure applies the set fields in a fixed order and stops at the
// first error; fields applied before it stay applied.
func (s *Server) Configure(_ context.Context, in *pb.Settings) (*pb.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.GainTenthDb != nil && !slices.Contains(tuner.GainSteps[:], int(in.GetGainTenthDb())) {
		return nil, status.Errorf(codes.InvalidArgument, "gain %d not in table", in.GetGainTenthDb())
	}
	if in.DirectSampling != nil && in.GetDirectSampling() != pb.DirectSampling_DIRECT_SAMPLING_OFF && !s.dev.BlogV4() {
		return nil, status.Error(codes.Unimplemented, "direct sampling")
	}
	err := s.apply(in)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return proto.CloneOf(s.st), nil
}

func (s *Server) apply(in *pb.Settings) error {
	if in.Ppm != nil {
		if err := s.dev.SetPPM(float64(in.GetPpm())); err != nil {
			return err
		}
		s.st.Ppm = proto.Int32(in.GetPpm())
		s.gen.Add(1)
		// The tuner LO depends on the correction: retune, as rtl_tcp does.
		if in.FrequencyHz == nil && s.st.GetFrequencyHz() != 0 {
			if err := s.dev.SetFrequency(float64(s.st.GetFrequencyHz())); err != nil {
				return err
			}
		}
	}
	if in.SampleRateHz != nil {
		rate := in.GetSampleRateHz()
		exact, err := s.dev.SetSampleRate(rate)
		if err != nil {
			return err
		}
		// Match the tuner filter to the rate, as librtlsdr does.
		if err := s.dev.SetBandwidth(0, float64(rate)); err != nil {
			return err
		}
		s.st.SampleRateHz = proto.Uint32(rate)
		s.st.SampleRateExactHz = proto.Float64(exact)
		s.gen.Add(1)
	}
	if in.FrequencyHz != nil {
		if err := s.dev.SetFrequency(float64(in.GetFrequencyHz())); err != nil {
			return err
		}
		s.st.FrequencyHz = proto.Uint64(in.GetFrequencyHz())
		s.gen.Add(1)
	}
	if in.GainManual != nil {
		fn := s.dev.SetGainAuto
		if in.GetGainManual() {
			fn = s.dev.SetGainManual
		}
		if err := fn(); err != nil {
			return err
		}
		s.st.GainManual = proto.Bool(in.GetGainManual())
	}
	if in.GainTenthDb != nil {
		if err := s.dev.SetGain(int(in.GetGainTenthDb())); err != nil {
			return err
		}
		s.st.GainTenthDb = proto.Int32(in.GetGainTenthDb())
		s.st.GainManual = proto.Bool(true)
	}
	if in.Agc != nil {
		if err := s.dev.SetAGC(in.GetAgc()); err != nil {
			return err
		}
		s.st.Agc = proto.Bool(in.GetAgc())
	}
	if in.BiasTee != nil {
		if err := s.dev.SetBiasTee(in.GetBiasTee()); err != nil {
			return err
		}
		s.st.BiasTee = proto.Bool(in.GetBiasTee())
	}
	if in.DirectSampling != nil {
		// Blog V4 reaches HF through its upconverter: nothing to switch.
		s.st.DirectSampling = in.GetDirectSampling().Enum()
		s.gen.Add(1)
	}
	return nil
}

func (s *Server) Stream(req *pb.StreamRequest, ss grpc.ServerStreamingServer[pb.Frame]) error {
	minBits, maxBits := int(req.GetMinBits()), int(req.GetMaxBits())
	if minBits == 0 {
		minBits = 2
	}
	if maxBits == 0 {
		maxBits = 8
	}
	if minBits < 1 || maxBits > 8 || minBits > maxBits {
		return status.Errorf(codes.InvalidArgument, "bits %d..%d", minBits, maxBits)
	}

	ctx, cancel := context.WithCancelCause(ss.Context())
	defer cancel(nil)
	me := s.takeover(cancel)
	if ctx.Err() != nil {
		s.release(me)
		return streamErr(ctx)
	}

	r, err := s.dev.Samples(ctx)
	if err != nil {
		s.release(me)
		return status.Error(codes.Internal, err.Error())
	}
	q := newQueue(queueFrames)
	var wg sync.WaitGroup
	wg.Go(func() {
		// Releasing here, not when the handler returns: a handler
		// stuck in Send must not block the next stream.
		defer s.release(me)
		read(ctx, cancel, r, q)
	})
	defer wg.Wait()

	ctrl := newController(minBits, maxBits, holdUp)
	for {
		f, err := q.get(ctx)
		if err != nil {
			return streamErr(ctx)
		}
		bits := ctrl.update(q.len(), time.Now())
		data, step, err := codec.Encode(nil, f.raw, bits)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		out := &pb.Frame{
			FirstSample:      proto.Uint64(f.first),
			Count:            proto.Uint32(uint32(len(f.raw) / 2)),
			Bits:             proto.Uint32(uint32(bits)),
			SettingsGen:      proto.Uint64(s.gen.Load()),
			Data:             data,
			CapturedUnixNano: proto.Int64(f.captured.UnixNano()),
		}
		if bits < 8 {
			out.Step = proto.Float32(step)
		}
		if err := ss.Send(out); err != nil {
			return err
		}
	}
}

// read fills frames from rc until ctx ends or rc fails.
func read(ctx context.Context, cancel context.CancelCauseFunc, rc io.Reader, q *queue) {
	var first uint64
	for {
		raw := make([]byte, FrameBytes)
		if _, err := io.ReadFull(rc, raw); err != nil {
			if ctx.Err() == nil {
				cancel(fmt.Errorf("%w: %v", errSamples, err))
			}
			return
		}
		q.put(frame{raw: raw, first: first, captured: time.Now()})
		first += FrameBytes / 2
	}
}

// takeover makes a new session active, cancels the previous one and
// waits until its reader has released the device.
func (s *Server) takeover(cancel context.CancelCauseFunc) *session {
	me := &session{cancel: cancel, done: make(chan struct{})}
	s.smu.Lock()
	old := s.active
	s.active = me
	s.smu.Unlock()
	if old != nil {
		old.cancel(errSuperseded)
		<-old.done
	}
	return me
}

func (s *Server) release(me *session) {
	s.smu.Lock()
	if s.active == me {
		s.active = nil
	}
	s.smu.Unlock()
	close(me.done)
}

func streamErr(ctx context.Context) error {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, errSuperseded):
		return status.Error(codes.Aborted, "superseded")
	case errors.Is(cause, errSamples):
		return status.Error(codes.Internal, cause.Error())
	default:
		return status.FromContextError(ctx.Err()).Err()
	}
}
