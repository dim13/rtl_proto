// Package codec requantizes u8 I/Q frames to fewer bits per component.
//
// At 8 bits a frame is the raw bytes. Below 8 bits, a symmetric uniform
// quantizer around the u8 zero level 127.5 spans the frame's largest
// deviation, with a step of at least 1 so quiet frames stay lossless.
// Codes are packed MSB first, I and Q interleaved.
package codec

import (
	"errors"
	"math"
)

var (
	ErrBits  = errors.New("bits out of range")
	ErrShort = errors.New("short data")
)

const zero = 127.5

// Encode appends the packed codes of raw at the given bit depth to
// dst[:0] and returns them with the quantizer step (0 at 8 bits).
func Encode(dst, raw []byte, bits int) ([]byte, float32, error) {
	if bits < 1 || bits > 8 {
		return nil, 0, ErrBits
	}
	dst = dst[:0]
	if bits == 8 {
		return append(dst, raw...), 0, nil
	}
	var lo, hi byte = 255, 0
	for _, x := range raw {
		lo, hi = min(lo, x), max(hi, x)
	}
	a := max(zero-float64(lo), float64(hi)-zero)
	half := 1 << (bits - 1)
	step := float32(max(1, 2*a/float64(2*half)))

	var codes [256]byte
	for x := range codes {
		c := math.Floor((float64(x) - zero) / float64(step))
		codes[x] = byte(int(max(-float64(half), min(float64(half-1), c))) + half)
	}

	var acc uint32
	var n int
	for _, x := range raw {
		acc = acc<<bits | uint32(codes[x])
		n += bits
		if n >= 8 {
			n -= 8
			dst = append(dst, byte(acc>>n))
		}
	}
	if n > 0 {
		dst = append(dst, byte(acc<<(8-n)))
	}
	return dst, step, nil
}

// Decode fills dst with the components reconstructed from data.
func Decode(dst, data []byte, bits int, step float32) error {
	if bits < 1 || bits > 8 {
		return ErrBits
	}
	if len(data) < (len(dst)*bits+7)/8 {
		return ErrShort
	}
	if bits == 8 {
		copy(dst, data)
		return nil
	}
	half := 1 << (bits - 1)
	var levels [256]byte
	for c := range 2 * half {
		v := math.Round(zero + (float64(c-half)+0.5)*float64(step))
		levels[c] = byte(max(0, min(255, v)))
	}

	mask := uint32(1)<<bits - 1
	var acc uint32
	var n, j int
	for i := range dst {
		for n < bits {
			acc = acc<<8 | uint32(data[j])
			j++
			n += 8
		}
		n -= bits
		dst[i] = levels[acc>>n&mask]
	}
	return nil
}
