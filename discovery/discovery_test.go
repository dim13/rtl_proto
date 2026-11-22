package discovery

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestFind(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go Serve(pc, 7373)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := Find(ctx, pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if want := "127.0.0.1:7373"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindNoAnswer(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close() // nobody answers on it

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Find(ctx, pc.LocalAddr().String()); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestServeIgnoresJunk(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go Serve(pc, 7373)

	c, err := net.Dial("udp4", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := c.Read(make([]byte, 64)); err == nil {
		t.Errorf("got %d byte reply to junk, want none", n)
	}
}
