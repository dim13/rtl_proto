# rtl_proto design

Date: 2026-11-22. Status: draft for review.

## Intent

Stream an RTL-SDR (RTL-SDR Blog V4 on host `nuc`) over limited networks
such as WiFi at the sample rates clients ask for, using one formal,
versioned wire protocol defined in protobuf. Existing clients (SDR++,
Gqrx, gr-osmosdr) keep working unchanged.

rtl_tcp cannot do this: it streams raw 8-bit I/Q at 2 bytes per sample
(4.8 MB/s at 2.4 MS/s), has no replies, no state query, no versioning
and no compression. The measured nuc-to-Mac link carries about
2.3 MB/s.

## Goals

- Two programs in one repo:
  - **rtl_remote** runs next to the dongle, drives it through
    `github.com/dim13/r82xx`, and serves the new protocol.
  - **rtl_proxy** runs next to the client, speaks the new protocol to
    rtl_remote, and serves plain rtl_tcp on localhost.
- Fit the client's requested sample rate through a constrained link by
  **adaptive requantization**: fewer bits per sample when the link is
  saturated, back up to 8 bits (bit-exact) when it is not.
- A formal protocol: gRPC service and messages in a versioned `.proto`
  package, typed replies and errors, device capability query.

## Success criteria

- At 2.4 MS/s over the nuc-to-Mac link (about 2.3 MB/s), SDR++ connected
  to rtl_proxy plays an FM broadcast continuously. The bits-per-sample
  value reported by the frames drops below 8 to make it fit.
- Over a link with ample capacity (e.g. loopback, or nuc-to-nuc), frames
  settle at 8 bits and the u8 stream the client receives is bit-exact
  with what the dongle produced.
- Latency is the priority after fitting the link: keep it as low as
  possible. Target: under 100 ms added between USB read on rtl_remote
  and the write to the client on rtl_proxy, on the local network at
  steady state, measured from `Frame.captured_unix_nano` (both hosts
  NTP-synced). When the link cannot keep up even at the minimum bit
  depth, samples are dropped rather than queued.

## Non-goals

- Multiple simultaneous streaming clients or fan-out.
- Server-side decimation, digital down-conversion, or demodulation.
- A native SDR++ / GNU Radio module (enabled later by the gRPC choice,
  not part of this work).
- Direct sampling on non-V4 hardware (r82xx does not support it).
- Recording, playback, or a web UI.

## Anti-goals

These would make the work a failure even if it technically runs:

- Any change required on the client side to get basic operation.
- Silent corruption at 8 bits: 8-bit frames must be bit-exact.
- Unbounded latency from queues growing under load.
- A protocol only this Go code can speak, e.g. framing or semantics
  outside what the `.proto` and this document define.

## Constraints

- Go 1.27, module `github.com/dim13/rtl_proto`, same conventions as the
  sibling repos rtl_tcp and r82xx (stdlib `flag`, no `internal/`, no
  Makefile).
- Dependencies: `google.golang.org/grpc`, `google.golang.org/protobuf`,
  `github.com/dim13/r82xx`,
  `github.com/google/gousb` (remote only).
- rtl_remote runs on the nuc (Void Linux, x86-64), with modest CPU.
- Device control is whatever r82xx exposes today: frequency, sample
  rate (+ bandwidth), PPM, gain auto/manual, gain value, demod AGC,
  bias tee, `Chip()`, `BlogV4()`.
- The rtl_tcp behaviour the proxy must reproduce is specified in
  `../rtl_tcp/docs/rtl_tcp-protocol-rfc.txt` and implemented in
  `../rtl_tcp/main.go`.

## Approaches considered

- **A. Plain TCP with varint-length-delimited protobuf messages**
  (`protodelim`). Smallest, direct TCP backpressure. Rejected: framing
  and request/reply semantics would be custom, and no codegen for other
  languages.
- **B. gRPC streaming (chosen).** Service defined in the `.proto`;
  status codes, deadlines, metadata, TLS, per-message compression and
  generated clients for other languages come with it. Costs: a large
  dependency tree, HTTP/2 flow control between the sender and the link,
  and TCP head-of-line blocking on lossy WiFi.
- **C. QUIC with datagrams.** Best on lossy WiFi (lost frames are gaps,
  not stalls) and gives congestion-window feedback. Rejected for now:
  no formal service definition and weaker codegen story. The messages
  below are transport-neutral, so C could be added later.

The user chose B for the formal service definition and multi-language
codegen.

## Architecture

```
 [client]  --rtl_tcp-->  [rtl_proxy]  ==gRPC/HTTP2==>  [rtl_remote]  --USB-->  [V4 dongle]
 SDR++/Gqrx  127.0.0.1:1234   Mac                         nuc          r82xx
```

rtl_remote:

1. Opens the dongle (USB code copied from `../rtl_tcp/usb.go`).
2. A reader goroutine reads USB transfers into fixed-size frames of
   raw u8 I/Q and puts them on a small bounded channel (a few frames,
   tens of milliseconds). When full, the oldest frame is dropped so
   what gets sent is always the freshest. USB transfers are one frame
   (16 KiB) each, not rtl_tcp's 256 KiB, which alone would add about
   55 ms at 2.4 MS/s.
3. The `Stream` RPC handler takes frames from the channel, encodes each
   at the current bit depth, and sends it. A controller adjusts the bit
   depth from queue occupancy (see Adaptation).
4. `Configure` and `Info` RPCs call r82xx directly.

rtl_proxy:

1. Dials rtl_remote, calls `Info` once.
2. Listens for rtl_tcp clients (default `localhost:1234`, every loopback address it resolves to), one at a
   time, like rtl_tcp.
3. On client connect: sends the 12-byte `RTL0` greeting built from
   `Info` (tuner type, gain count), opens `Stream`, decodes frames back
   to u8 I/Q and writes them to the client.
4. Each 5-byte rtl_tcp command becomes a `Configure` call with the
   matching field set. Errors are logged, never sent to the client
   (rtl_tcp has no replies).

## Protocol

Package `rtlproto.v1`. Sketch (field numbers final once reviewed):

```proto
syntax = "proto3"; // optional on every singular field: explicit presence
package rtlproto.v1;
option go_package = "github.com/dim13/rtl_proto/rtlprotov1";

service Receiver {
  // Device identity and capabilities.
  rpc Info(InfoRequest) returns (DeviceInfo);
  // Apply the fields that are set; return the effective state.
  rpc Configure(Settings) returns (Settings);
  // Sample stream. At most one active stream per remote.
  rpc Stream(StreamRequest) returns (stream Frame);
}

message InfoRequest {}

message DeviceInfo {
  optional TunerType tuner = 1;
  optional bool blog_v4 = 2;
  repeated int32 gains_tenth_db = 3; // gain table, ascending
  optional string manufacturer = 4;
  optional string product = 5;
  Settings current = 6;
}

enum TunerType {   // values match librtlsdr / rtl_tcp greeting
  TUNER_TYPE_UNKNOWN = 0;
  TUNER_TYPE_E4000 = 1;
  TUNER_TYPE_FC0012 = 2;
  TUNER_TYPE_FC0013 = 3;
  TUNER_TYPE_FC2580 = 4;
  TUNER_TYPE_R820T = 5;
  TUNER_TYPE_R828D = 6;
}

// Explicit presence: unset fields are left unchanged.
message Settings {
  optional uint64 frequency_hz = 1;
  optional uint32 sample_rate_hz = 2;   // reply carries the requested rate;
  optional double sample_rate_exact_hz = 3; // reply only
  optional sint32 ppm = 4;
  optional bool gain_manual = 5;
  optional sint32 gain_tenth_db = 6;
  optional bool agc = 7;                // demodulator digital AGC
  optional bool bias_tee = 8;
  optional DirectSampling direct_sampling = 9;
}

enum DirectSampling {
  DIRECT_SAMPLING_OFF = 0;
  DIRECT_SAMPLING_I = 1;
  DIRECT_SAMPLING_Q = 2;
}

message StreamRequest {
  optional uint32 min_bits = 1; // 1..8, 0 means default (2)
  optional uint32 max_bits = 2; // 1..8, 0 means 8
}

message Frame {
  optional uint64 first_sample = 1;  // index of first complex sample since stream start
  optional uint32 count = 2;         // complex samples in this frame
  optional uint32 bits = 3;          // bits per component, 1..8
  optional float step = 4;           // quantizer step in u8 units (bits < 8)
  optional uint64 settings_gen = 5;  // incremented by every Configure that changed tuning
  optional bytes data = 6;           // packed codes, I and Q interleaved
  optional int64 captured_unix_nano = 7; // when rtl_remote read the frame from USB
}
```

Error semantics (gRPC status codes):

- `InvalidArgument`: value out of range (e.g. gain not in table, bits
  outside 1..8).
- `Unimplemented`: setting not supported by this device (e.g.
  direct sampling I/Q on non-V4).
- `Aborted`: sent to an active `Stream` when a newer `Stream` takes it
  over. A new `Stream` always wins, so a proxy reconnecting after a
  half-open WiFi connection is never locked out. rtl_remote also runs
  server-side keepalive so dead streams are noticed on their own.
- `Internal`: USB or tuner failure (e.g. PLL did not lock).

Compression: gRPC's built-in gzip compressor
(`google.golang.org/grpc/encoding/gzip`, stdlib `compress/gzip` at
`BestSpeed`), requested by the proxy on `Stream`. It applies on top of
requantization. Measured per 16 KiB frame on real captures from the
nuc: noisy 8-bit frames barely compress (0.97 to 0.98), quiet ones
reach 0.30; at 2 to 4 bits the codes cluster near zero and gzip
roughly halves them again (e.g. 0.50 to 0.24 at 4 bits). zstd
(klauspost, fastest) gave the same ratios, so the extra dependency
was dropped.

## Sample codec

Input: u8 I/Q, zero level 127.5.

- **bits = 8**: `data` is the raw u8 bytes, `step` unset. Bit-exact.
- **bits < 8**: per frame, a symmetric uniform quantizer around 127.5:
  - `step = max(1, (2*A) / 2^bits)` where `A` is the frame's maximum
    absolute deviation from 127.5 (clipping at a few standard deviations
    instead of the maximum is a possible refinement, left to the
    builder with measurements).
  - code `c = clamp(floor((x - 127.5) / step), -2^(bits-1), 2^(bits-1)-1)`
  - reconstruction `x' = 127.5 + (c + 0.5) * step`, rounded and clamped
    to 0..255 by the proxy.
  - With `step = 1` the round trip is lossless, so a quiet frame stays
    exact at fewer bits.
  - Codes are packed MSB-first, I and Q interleaved, padded to a byte at
    the end of the frame.
- Frame size: 8192 complex samples (16 KiB raw, about 3.4 ms at
  2.4 MS/s), small for latency but large enough for gzip and to keep
  the message rate (about 300/s at 2.4 MS/s) cheap.

The codec lives in its own package with table-driven tests: bit-exact
round trip at 8 bits, error at most `step/2` for every sample at lower
depths, and packing/unpacking for every bit depth 1 to 8.

## Adaptation

The controller runs in rtl_remote, per stream:

- Signal: occupancy of the bounded channel between the USB reader and
  the send loop. `Send` blocking (HTTP/2 window exhausted, TCP send
  buffer full) makes the channel fill. For this signal to arrive
  quickly, the buffers below the channel are kept small: gRPC's
  automatic window growth (BDP probing) is turned off and the HTTP/2
  stream/connection windows and the TCP send buffer are pinned to a
  few frames' worth. Otherwise data piles up in the network stack,
  out of the controller's sight, and latency grows.
- Step down one bit as soon as the channel holds more than one frame
  waiting; step up one bit after it has stayed empty for a hold time
  (e.g. 2 s). Bounded by `min_bits` and `max_bits` from
  `StreamRequest`. Reacting fast and recovering slowly keeps latency
  low at the cost of dropping bits on short WiFi dips.
- At `min_bits` with the channel full, the oldest frame is dropped.
  The gap is visible to the proxy through `first_sample`. The proxy
  does not fill gaps (rtl_tcp clients cannot detect them anyway) and
  logs them once per second, like rtl_tcp.
- rtl_proxy writes each decoded frame to the client immediately, with
  no buffering of its own beyond one frame.

## Proxy details

- Retunes are visible through `settings_gen`, but the proxy does not
  act on them in this version.
- If the gRPC connection fails, the proxy keeps the rtl_tcp client
  connected, reconnects with backoff, re-applies the last settings it
  sent and opens a new `Stream`.
- rtl_tcp commands not mapped to `Settings` (test mode, offset tuning,
  crystal frequencies, IF gain) are logged and ignored, with the same
  "off is accepted" rule rtl_tcp uses today.
- Gain by index (0x0d) is resolved locally using `gains_tenth_db` from
  `Info`.

## Security

The target is a trusted local network, so the user chose no encryption
and no authentication, like rtl_tcp:

- Plaintext gRPC (`insecure.NewCredentials()`).
- rtl_remote listens on all interfaces (`:7373`) by default: serving
  the network is its only job. Restrict it with `-a` if needed.
- Accepted risk: anyone who can reach the port can listen, retune, and
  switch the bias tee (DC onto the antenna). Use network-level access
  control or an SSH tunnel if the network is not trusted.

## Command lines

```
rtl_remote [-a :7373] [-f 100e6 -s 2048000 -g 0 -P 0] [-d]
rtl_proxy  [-a localhost:1234] [-min-bits 2 -max-bits 8] [-d] [host[:7373]]
```

`-d` logs received commands, as in rtl_tcp. `-min-bits 8` forces
lossless.

## Discovery

With no host argument, rtl_proxy finds rtl_remote on the local
network, so neither side needs flags in the common case:

- rtl_remote answers on UDP at the same address and port as gRPC
  (default `:7373`).
- rtl_proxy broadcasts the ASCII query `rtl_proto?` to
  `255.255.255.255:7373` and waits up to 2 s. The first answer,
  `rtl_proto <port>`, gives the gRPC port; its source address gives
  the host. Other datagrams are ignored.
- IPv4 broadcast only, so it stays within the local subnet. A host
  given as argument skips discovery.

## Repository layout

```
rtl_proto/
  go.mod
  proto/rtlproto/v1/receiver.proto
  rtlprotov1/            generated *.pb.go, *_grpc.pb.go (committed)
  codec/                 requantization + packing, tests
  cmd/rtl_remote/        main.go, usb.go (copied from rtl_tcp)
  cmd/rtl_proxy/         main.go
  docs/
```

Code generation via a `//go:generate protoc ...` line using the
installed `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`.

## Left to the builder

- Exact window and buffer sizes, channel depth, and hold time, tuned on
  the real link toward the latency target (BDP probing stays off).
- Keepalive intervals.
- Whether to clip at k standard deviations instead of the maximum.
- Logging detail, beyond the once-per-second drop summary and an
  latency readout in `-d` mode.

## Calls made without explicit user input

Correct any of these:

- One `Configure` RPC relying on explicit field presence (proto3 `optional`) instead of one RPC per
  setting.
- Requantization per frame with a symmetric uniform quantizer, step
  floor 1 so quiet signals stay lossless; default `min_bits` 2.
- gzip via gRPC per-message compression, not inside the frame.
- Frame size 8192 complex samples, one USB transfer per frame.
- Latency target under 100 ms on the local network; drop oldest, not
  newest.
- No gap filling in the proxy.
- A new `Stream` takes over an active one (old one ends with
  `Aborted`) instead of being refused as busy.
- Proxy reconnects transparently and re-applies settings.
- Remote listens on all interfaces by default (changed at the user's
  request); the proxy finds it by UDP broadcast, or adds port 7373 to
  a host given as argument.
- Port 7373 as default for rtl_remote.
