package grpczstd

import (
	"bytes"
	"io"
	"testing"

	"google.golang.org/grpc/encoding"
)

func TestRoundTrip(t *testing.T) {
	c := encoding.GetCompressor(Name)
	if c == nil {
		t.Fatal("compressor not registered")
	}
	in := make([]byte, 64<<10)
	for i := range in {
		in[i] = byte(i % 7)
	}
	var buf bytes.Buffer
	w, err := c.Compress(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(in); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() >= len(in) {
		t.Errorf("compressed %d bytes, want < %d", buf.Len(), len(in))
	}
	r, err := c.Decompress(&buf)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, in) {
		t.Error("round trip mismatch")
	}
}
