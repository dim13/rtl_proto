// Package grpczstd registers a zstd compressor with gRPC. Import it for
// side effects and select it with grpc.UseCompressor(grpczstd.Name).
package grpczstd

import (
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc/encoding"
)

var (
	encoders = sync.Pool{New: func() any {
		e, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
		return e
	}}
	decoders = sync.Pool{New: func() any {
		d, _ := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		return d
	}}
)

// Name is the gRPC compressor name.
const Name = "zstd"

type compressor struct{}

type writer struct {
	*zstd.Encoder
}

type reader struct {
	d *zstd.Decoder
}

func init() {
	encoding.RegisterCompressor(compressor{})
}

func (compressor) Name() string {
	return Name
}

func (compressor) Compress(w io.Writer) (io.WriteCloser, error) {
	e := encoders.Get().(*zstd.Encoder)
	e.Reset(w)
	return writer{e}, nil
}

func (compressor) Decompress(r io.Reader) (io.Reader, error) {
	d := decoders.Get().(*zstd.Decoder)
	if err := d.Reset(r); err != nil {
		decoders.Put(d)
		return nil, err
	}
	return &reader{d: d}, nil
}

func (w writer) Close() error {
	err := w.Encoder.Close()
	encoders.Put(w.Encoder)
	return err
}

// Read returns the decoder to the pool once the message is consumed.
func (r *reader) Read(p []byte) (int, error) {
	if r.d == nil {
		return 0, io.EOF
	}
	n, err := r.d.Read(p)
	if err == io.EOF {
		decoders.Put(r.d)
		r.d = nil
	}
	return n, err
}
