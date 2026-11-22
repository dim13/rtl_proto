# Spectrum and Channel Streams Implementation Plan (rtl_gui phase 1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add remote-computed `Spectrum` and narrowband `Channel` streams to rtl_proto, share one USB reader among all streams, and make the USB glue importable, so rtl_gui can be built on top.

**Architecture:** A `dsp` package (FFT, Hann window, NCO, windowed-sinc FIR, polyphase decimator; stdlib only) does the math. `remote.Server` gets a hub that reads USB once and fans raw frames out to per-consumer drop-oldest queues; `Stream` (with its takeover rule), `Spectrum` and `Channel` (up to 8 each, independent) subscribe to it. `cmd/rtl_remote`'s USB code moves to `usbdev`.

**Tech Stack:** Go 1.27.4, gRPC, protobuf (proto3 + `optional`), existing rtl_proto packages.

**Spec:** `/Users/dima/go/src/github.com/dim13/rtl_gui/docs/superpowers/specs/2026-11-22-rtl-gui-design.md` (phase 1 sections). Background: `docs/superpowers/specs/2026-11-22-rtl-proto-design.md`.

## Global Constraints

- Go 1.27.4, stdlib `flag`/`log/slog`, no `internal/`, no Makefile, table-driven stdlib tests, `go test -race ./...` green after every task.
- `dsp` is stdlib only; samples are `complex64`; input u8 maps to `(x-127.5)/127.5`.
- Proto stays `syntax = "proto3"` with `optional` on every singular field; field names/numbers exactly as in the spec's "Protocol additions".
- `SpectrumLine.power`: byte `v` = `(v/2) - 127.5` dBFS, `v = clamp(round((dB+127.5)*2), 0, 255)`; lowest frequency first, DC in the middle. 0 dBFS = full-scale complex tone.
- `SpectrumRequest` defaults: bins 2048 (power of two 64..65536), lines_per_second 25 (1..60).
- `ChannelFrame.iq`: int16 little-endian, I/Q interleaved, 32767 = input full scale.
- `ChannelRequest.bandwidth_hz` 0 means 0.8 × rate. Device rate must be an integer multiple of the channel rate; `|offset_hz| <= rate/2`.
- FIR: passband ripple ≤ 0.5 dB, stopband ≥ 60 dB.
- Limits: at most 8 `Spectrum` and 8 `Channel` streams at once → `ResourceExhausted`. Takeover only for `Stream`.
- Configure while open: center change → keep running; rate change → `Spectrum` keeps running, `Channel` rebuilds if still valid, else ends `FailedPrecondition`.
- rtl_remote default sample rate 2 400 000.
- Commit messages: short subject, no AI attribution. Never push without explicit permission.

## Review Focus

1. A slow `Spectrum` or `Channel` client must not stall `Stream` or the USB reader (per-consumer drop-oldest). Test in Task 4.
2. Offset update on a running `Channel`: output tone moves without the stream ending, NCO phase continuous. Test in Task 6.
3. Device rate changed by `Configure` under an open `Channel` whose rate no longer divides it → `FailedPrecondition`; compatible change keeps flowing. Test in Task 6.
4. Last subscriber leaving stops the USB reader; a new subscriber restarts it (no leaked reader goroutine, fake `Released` increments). Test in Task 4.
5. Ninth `Channel` → `ResourceExhausted`, and closing one frees a slot. Test in Task 6.

---

### Task 1: dsp FFT and window

**Files:**
- Create: `dsp/fft.go`, `dsp/fft_test.go`

**Interfaces:**
- Produces: `func FFT(x []complex64)` (in place, `len(x)` power of two, panics otherwise — programmer error); `func Hann(n int) []float32`; `func Shift(x []complex64)` (FFT-shift in place so DC is at index n/2).

- [ ] **Step 1: Write failing tests**: `TestFFTMatchesDFT` (random input, n = 8, 64, 1024: each bin within 1e-3 relative of a direct O(n²) DFT computed in float64 in the test); `TestFFTTone` (complex tone at bin k=5 of n=64 → `|X[5]| ≈ 64`, other bins < 1e-3); `TestHannSymmetric` (`Hann(8)`: w[0]==0, w[i]==w[8-i] for i in 1..7, peak at 4 equals 1 — periodic Hann); `TestShift` (`[0,1,2,3]` → `[2,3,0,1]`).
- [ ] **Step 2: Run** `go test ./dsp` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** iterative radix-2 decimation-in-time with bit reversal; twiddles computed in float64 and cached per size (`sync.Map` keyed by n).
- [ ] **Step 4: Run** `go test -race ./dsp` — Expected: PASS. Add `BenchmarkFFT2048` (`for b.Loop()`), run once, note ns/op in commit body.
- [ ] **Step 5: Commit** `git add dsp && git commit -m "Add FFT"`

---

### Task 2: dsp NCO, FIR design, polyphase decimator

**Files:**
- Create: `dsp/nco.go`, `dsp/fir.go`, `dsp/dsp_test.go`

**Interfaces:**
- Produces:
  - `type NCO struct{...}`; `func NewNCO(freq float64) *NCO` (freq in cycles per sample, negative allowed); `func (n *NCO) SetFreq(freq float64)` (phase continuous); `func (n *NCO) Mix(x []complex64)` (multiply in place by e^{j·2π·freq·k}).
  - `func Lowpass(cutoff float64, taps int) []float32` — windowed sinc (Blackman-Harris or Kaiser, builder's choice meeting the Global Constraints), cutoff in cycles per sample (0..0.5), unity DC gain.
  - `func TapsFor(cutoff, transition float64) int` — odd tap count achieving ≥ 60 dB stopband for the chosen window and the given transition width (cycles per sample).
  - `type Decimator struct{...}`; `func NewDecimator(taps []float32, factor int) *Decimator`; `func (d *Decimator) Process(dst, src []complex64) []complex64` — appends filtered, decimated output of `src` to `dst[:0]`, keeping history across calls so consecutive calls equal one call on the concatenation.

- [ ] **Step 1: Write failing tests**:
  - `TestNCOShift`: tone at +0.1 cycles/sample, `NewNCO(-0.1).Mix` → output ≈ constant (DC), max deviation < 1e-3.
  - `TestNCOPhaseContinuous`: mix 1000 samples in one call vs. 10 calls of 100 → identical within 1e-5; after `SetFreq` mid-stream, the sample-to-sample phase step changes but no phase jump (|Δphase − 2π·newfreq| < 1e-4 at the switch).
  - `TestLowpassResponse`: `taps := Lowpass(0.1, TapsFor(0.1, 0.05))`; evaluate |H(f)| by DFT of taps at 512 points: passband f ≤ 0.1 ripple ≤ 0.5 dB, stopband f ≥ 0.15 ≤ −60 dB, DC gain 1 ± 1e-3.
  - `TestDecimatorChunked`: random input of 5000 samples, factor 7: one `Process` call equals concatenated outputs of calls on chunks of sizes 1, 13, 999, rest (within 1e-5); output length totals `floor` consistent count (same as single call).
  - `TestDecimatorRejectsAlias`: tone at 0.3 cycles/sample through `Lowpass(0.04, TapsFor(0.04,0.02))`, factor 10 → output power ≤ −60 dB relative to input power; tone at 0.01 passes with gain 1 ± 0.06 (0.5 dB).
- [ ] **Step 2: Run** `go test ./dsp` — Expected: FAIL.
- [ ] **Step 3: Implement**; the decimator computes only every `factor`-th output (polyphase by index skipping, no wasted MACs).
- [ ] **Step 4: Run** `go test -race ./dsp` — Expected: PASS.
- [ ] **Step 5: Commit** `git add dsp && git commit -m "Add NCO and decimating FIR"`

---

### Task 3: Fake device tone

**Files:**
- Modify: `remote/fake/fake.go`
- Create: `remote/fake/fake_test.go`

**Interfaces:**
- Produces: new field `Tone float64` on `fake.Device`: when non-zero, the sample stream is a complex tone at `Tone` Hz relative to center, at the rate last passed to `SetSampleRate` (default 2 400 000 if never set), amplitude 100 (of ±127.5), encoded as u8 I/Q (`round(127.5 + 100·cos)`, `round(127.5 + 100·sin)`), phase continuous across readers. `Tone == 0` keeps today's counter pattern (all existing tests rely on it).

- [ ] **Step 1: Write failing test `TestToneFrequency`**: `d := &fake.Device{Tone: 120e3}`; `d.SetSampleRate(2_400_000)`; read 2400 complex samples via `Samples(ctx)`; estimate frequency from mean phase increment → 120 kHz ± 100 Hz.
- [ ] **Step 2: Run** `go test ./remote/fake` — Expected: FAIL (unknown field Tone).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./remote/...` — Expected: PASS (existing tests unchanged).
- [ ] **Step 5: Commit** `git add remote/fake && git commit -m "Add tone to fake device"`

---

### Task 4: Shared USB reader (hub)

**Files:**
- Create: `remote/hub.go`, `remote/hub_test.go`
- Modify: `remote/server.go`, `remote/queue.go`

**Interfaces:**
- Consumes: `remote.Device`, `queue`, `frame` (Task 4 of the rtl_proto plan, existing).
- Produces:
  - `type hub struct{...}`; `func newHub(dev Device) *hub`.
  - `func (h *hub) subscribe(n int) (*queue, func())` — returns a new drop-oldest queue of depth `n` receiving every raw frame from now on, and an unsubscribe func. The first subscriber starts the USB reader (`dev.Samples(ctx)`); the last unsubscribe cancels it and waits for it to exit.
  - `frame.first` becomes the hub-global complex-sample index; consumers rebase to their own start.
  - `func (q *queue) fail(err error)` and `get` returning that error once the queue is empty — a reader failure reaches every subscriber.
  - `Server` gains a `hub` field; `Stream` subscribes instead of calling `Samples` itself. Takeover for `Stream` stays (newest wins, old ends `Aborted`), but no longer waits for the old stream to release the device.

- [ ] **Step 1: Write failing tests** (`remote/hub_test.go`, with `fake.Device{Pace: time.Millisecond}`):
  - `TestHubFanOut`: two subscribers each receive the same `first` sequence for 5 frames.
  - `TestHubSlowConsumer` (Review Focus 1): subscriber A never reads, subscriber B reads 50 frames within 1 s; A's queue length stays ≤ its depth.
  - `TestHubStopsReader` (Review Focus 4): subscribe, read 1 frame, unsubscribe → fake `Released == 1` within 1 s; subscribe again → frames flow, and after unsubscribing `Released == 2`.
  - `TestHubReaderError`: `FailAfter: 3` → subscriber's `get` returns a non-nil error after at most 3 frames.
  - Server level, in `remote/server_test.go`: `TestStreamAndSecondConsumer`: a `Stream` keeps delivering while a hub subscriber (test-internal) reads too.
- [ ] **Step 2: Run** `go test ./remote` — Expected: FAIL.
- [ ] **Step 3: Implement** hub and switch `Stream` to it; `Frame.first_sample` stays "since this stream started" (rebase on the first frame received).
- [ ] **Step 4: Run** `go test -race -count=3 ./remote/... ./proxy` — Expected: PASS, including all existing Stream/takeover/reconnect tests.
- [ ] **Step 5: Commit** `git add remote && git commit -m "Share one USB reader among streams"`

---

### Task 5: Spectrum RPC

**Files:**
- Modify: `proto/rtlproto/v1/receiver.proto`, regenerate `rtlprotov1/*.pb.go` (`PATH=$(go env GOPATH)/bin:$PATH go generate ./rtlprotov1`)
- Create: `remote/spectrum.go`, `remote/spectrum_test.go`

**Interfaces:**
- Produces: proto additions exactly as in the spec (`Spectrum`, `Channel` RPCs; `SpectrumRequest`, `SpectrumLine`, `ChannelRequest`, `ChannelFrame`) — add both RPCs' messages now so the proto changes once; `Channel` stays unimplemented until Task 6. `func (s *Server) Spectrum(req *pb.SpectrumRequest, ss grpc.ServerStreamingServer[pb.SpectrumLine]) error`.

Behavior: validate bins/lines (defaults, `InvalidArgument` outside range), enforce the 8-stream limit (`ResourceExhausted`), subscribe to the hub, convert u8 → complex64, process `bins`-sized blocks (Hann window, `dsp.FFT`, accumulate |X|²), emit a line every `rate / lines_per_second` input samples (rate read from server state per line so rate changes apply), normalize so a full-scale tone is 0 dBFS (divide by `(Σw)²` and by the number of averaged blocks), `dsp.Shift`, quantize per the Global Constraints. Each line carries `center_hz`, `sample_rate_hz`, `settings_gen`, `captured_unix_nano` of its last frame.

- [ ] **Step 1: Write failing tests** (bufconn helpers from `server_test.go`):
  - `TestSpectrumTone`: `fake.Device{Tone: 300e3}` with `Configure{sample_rate_hz: 2_400_000}`, `bins: 1024` → peak bin index = `512 + round(300e3/2.4e6·1024)` = 640 (±1); peak value within 3 dB of the tone's dBFS (amplitude 100/127.5 → −2.1 dBFS); median bin at least 40 dB below the peak.
  - `TestSpectrumDefaults`: empty request → `len(power) == 2048`; ~25 lines/s (between 15 and 35 lines in 1 s with `Pace` set so the fake runs at real time: `Pace: 16384/2/2.4e6 s`).
  - `TestSpectrumInvalid`: bins 1000, bins 32, lines 0→default ok, lines 61 → `InvalidArgument` for the invalid ones.
  - `TestSpectrumRetune`: after `Configure{frequency_hz}` lines carry the new `center_hz` and a higher `settings_gen`.
  - `TestSpectrumLimit`: 8 open streams fine, 9th → `ResourceExhausted`.
- [ ] **Step 2: Run** `go test ./remote` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `git add -A && git commit -m "Add spectrum stream"`

---

### Task 6: Channel RPC

**Files:**
- Create: `remote/channel.go`, `remote/channel_test.go`

**Interfaces:**
- Consumes: `dsp.NCO`, `dsp.Lowpass`, `dsp.TapsFor`, `dsp.Decimator`, hub.
- Produces: `func (s *Server) Channel(ss grpc.BidiStreamingServer[pb.ChannelRequest, pb.ChannelFrame]) error`.

Behavior: the first `Recv` is the initial request (validate: rate > 0, device rate % rate == 0, |offset| ≤ device rate/2, 0 < bandwidth ≤ rate; `InvalidArgument` otherwise); enforce the 8-stream limit; subscribe; per frame: convert, `NCO(-offset/devRate).Mix`, decimate by `devRate/rate` with `Lowpass((bandwidth/2)/devRate, TapsFor(..., transition = (rate - bandwidth)/2/devRate, at least 0.05·rate/devRate))`, emit int16 frame with `offset_hz` in effect. A goroutine `Recv`s updates: any field set replaces the current value after the same validation (invalid update → stream ends `InvalidArgument`); a new offset applies at the next frame via `NCO.SetFreq`; a new rate or bandwidth rebuilds the decimator. Device rate change seen at a frame (compare server state): rebuild if still valid, else end `FailedPrecondition`. `first_sample` counts output samples since stream start.

- [ ] **Step 1: Write failing tests**:
  - `TestChannelTone`: device 2.4 MS/s, `Tone: 100e3`, request offset 100e3, rate 48 000 → output ≈ DC: mean phase increment < 2π·50/48000, magnitude ≈ 100/127.5·32767 within 1 dB.
  - `TestChannelOffsetUpdate` (Review Focus 2): same tone, start offset 90e3 (output tone at +10 kHz), send update offset 100e3 → later frames ≈ DC, stream never ended, `offset_hz` in frames switches from 90000 to 100000.
  - `TestChannelInvalid`: rate 50 000 (2.4M not divisible), offset 1.3e6, bandwidth 60 000 > rate → `InvalidArgument`.
  - `TestChannelRateChange` (Review Focus 3): open at rate 48 000 on 2.4 MS/s; `Configure{sample_rate_hz: 1_920_000}` → keeps flowing (1.92M divisible by 48k) with `sample_rate_hz` 48000; `Configure{sample_rate_hz: 2_048_000}` → stream ends `FailedPrecondition`.
  - `TestChannelLimit` (Review Focus 5): 8 open fine, 9th `ResourceExhausted`, close one → a new one succeeds.
  - `TestChannelWithStreamAndSpectrum`: all three stream types deliver concurrently for 1 s.
- [ ] **Step 2: Run** `go test ./remote` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -count=2 ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `git add -A && git commit -m "Add channel stream"`

---

### Task 7: usbdev package, rtl_remote default rate

**Files:**
- Create: `usbdev/usb.go` (moved from `cmd/rtl_remote/usb.go`), `usbdev/device.go` (moved from `cmd/rtl_remote/device.go`)
- Delete: `cmd/rtl_remote/usb.go`, `cmd/rtl_remote/device.go`
- Modify: `cmd/rtl_remote/main.go`

**Interfaces:**
- Produces: package `usbdev`: `type Device struct{ *r82xx.Device; ... }` implementing `remote.Device`; `func Open() (*Device, error)` (opens USB and `r82xx.Open`); `func (d *Device) Close() error` (closes r82xx device then USB). Behavior of the ordered ReadStream with 10 s per-read timeout unchanged.
- rtl_remote: uses `usbdev.Open`; `-s` default `2_400_000`.

- [ ] **Step 1: Move the files** with `git mv`, rename package to `usbdev`, export `Open`/`Close`, keep comments.
- [ ] **Step 2: Verify** `go vet ./... && go build ./... && go test -race ./...` — Expected: clean, PASS. `go run ./cmd/rtl_remote -h` shows `-s` default 2400000.
- [ ] **Step 3: Update docs**: rtl_proto design doc command-line section default rate → 2400000.
- [ ] **Step 4: Commit** `git add -A && git commit -m "Move USB glue to usbdev, default 2.4 MS/s"`

---

### Task 8: Hardware check on nuc

**Files:** none committed (a throwaway client under the scratchpad).

Ask the user before stopping whatever holds the dongle on nuc.

- [ ] **Step 1:** rsync the repo to nuc, build and start `rtl_remote` (no flags).
- [ ] **Step 2:** From the Mac, a throwaway Go client calls `Spectrum` (2048 bins) tuned to an FM broadcast frequency: peaks visible at 200 kHz-spaced stations in the dumped line (print top 5 bins as frequencies); measure bytes/s (expect ≈ 51 KB/s or less with gzip).
- [ ] **Step 3:** `Channel` at rate 240 000 on a station offset for 5 s alongside `Spectrum`: frames continuous (`first_sample` gaps logged), bytes/s ≈ 960 KB/s or less, write I/Q to a file and FM-demodulate offline (quadrature discriminator in the client) to a WAV for the user to listen to.
- [ ] **Step 4:** Report numbers; no commit unless a defect is found (then TDD fix in the owning task's package).
