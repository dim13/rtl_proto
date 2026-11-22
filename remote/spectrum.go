package remote

import (
	"math"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/dim13/rtl_proto/dsp"
	pb "github.com/dim13/rtl_proto/rtlprotov1"
)

// maxStreams bounds concurrent Spectrum and Channel streams, each.
const maxStreams = 8

// defaultRate applies until the first Configure sets a sample rate.
const defaultRate = 2_400_000

// limit counts streams of one kind.
type limit struct {
	n atomic.Int32
}

func (l *limit) acquire() bool {
	if l.n.Add(1) > maxStreams {
		l.n.Add(-1)
		return false
	}
	return true
}

func (l *limit) release() {
	l.n.Add(-1)
}

// tuning returns the current center frequency and sample rate.
func (s *Server) tuning() (center uint64, rate uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rate = s.st.GetSampleRateHz()
	if rate == 0 {
		rate = defaultRate
	}
	return s.st.GetFrequencyHz(), rate
}

func (s *Server) Spectrum(req *pb.SpectrumRequest, ss grpc.ServerStreamingServer[pb.SpectrumLine]) error {
	bins, lps := int(req.GetBins()), int(req.GetLinesPerSecond())
	if bins == 0 {
		bins = 2048
	}
	if lps == 0 {
		lps = 25
	}
	if bins < 64 || bins > 65536 || bins&(bins-1) != 0 {
		return status.Errorf(codes.InvalidArgument, "bins %d", bins)
	}
	if lps > 60 {
		return status.Errorf(codes.InvalidArgument, "lines per second %d", lps)
	}
	if !s.spectra.acquire() {
		return status.Error(codes.ResourceExhausted, "too many spectrum streams")
	}
	defer s.spectra.release()
	ctx := ss.Context()
	q, unsub := s.hub.subscribe(queueFrames)
	defer unsub()

	w := dsp.Hann(bins)
	var wsum float64
	for _, v := range w {
		wsum += float64(v)
	}
	// A full-scale tone on a bin center gives |X|² = (Σw)²: 0 dBFS.
	norm := wsum * wsum
	blk := make([]complex64, bins)
	acc := make([]float64, bins)
	var fill, blocks, samples int
	for {
		f, err := q.get(ctx)
		if err != nil {
			if ctx.Err() == nil {
				return status.Error(codes.Internal, err.Error())
			}
			return status.FromContextError(ctx.Err()).Err()
		}
		for i := 0; i < len(f.raw); i += 2 {
			re := (float32(f.raw[i]) - 127.5) / 127.5
			im := (float32(f.raw[i+1]) - 127.5) / 127.5
			blk[fill] = complex(re*w[fill], im*w[fill])
			if fill++; fill == bins {
				dsp.FFT(blk)
				for k, v := range blk {
					acc[k] += float64(real(v)*real(v) + imag(v)*imag(v))
				}
				blocks++
				fill = 0
			}
		}
		samples += len(f.raw) / 2
		center, rate := s.tuning()
		if blocks == 0 || samples < int(rate)/lps {
			continue
		}
		power := make([]byte, bins)
		for i := range power {
			p := acc[(i+bins/2)%bins] / float64(blocks) / norm // DC in the middle
			db := 10 * math.Log10(max(p, 1e-30))
			power[i] = byte(max(0, min(255, math.Round((db+127.5)*2))))
		}
		clear(acc)
		blocks, samples = 0, 0
		err = ss.Send(&pb.SpectrumLine{
			CenterHz:         proto.Uint64(center),
			SampleRateHz:     proto.Uint32(rate),
			SettingsGen:      proto.Uint64(s.gen.Load()),
			CapturedUnixNano: proto.Int64(f.captured.UnixNano()),
			Power:            power,
		})
		if err != nil {
			return err
		}
	}
}
