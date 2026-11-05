// Package aacopus transcodes MPEG-4 Audio (AAC) into Opus, so that streams
// published with AAC audio (the default of OBS, FFmpeg and most RTMP/SRT encoders)
// can be read with WebRTC, which doesn't support AAC.
//
// Transcoding is the only CPU-heavy operation the server performs on audio, so it's
// done the cheapest possible way:
//   - on demand: a transcoder exists only while at least one reader needs Opus;
//   - shared: there's at most one transcoder per stream, whose output is fanned out
//     to every reader, so cost doesn't grow with the number of readers;
//   - native codecs: FFmpeg's AAC decoder and libopus, both with SIMD, called once
//     per frame through cgo;
//   - CELT-only Opus (restricted low delay) at a moderate complexity, which skips
//     the speech / music analysis and SILK modes of the default application;
//   - no resampling when the decoded sample rate is natively supported by Opus
//     (48kHz, the default of OBS, and 8/12/16/24kHz);
//   - no allocations per frame, except the payload handed to readers.
//
// The codecs are linked only when building with CGO_ENABLED=1 and the aacopus tag.
// Other builds keep the previous behavior (no audio for AAC streams read with WebRTC).
package aacopus

import (
	"fmt"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"

	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

const (
	// Opus RTP timestamps always use a 48kHz clock (RFC 7587).
	opusClockRate = 48000

	opusFrameDuration = 20 * time.Millisecond
	opusComplexity    = 5
	opusBitrateMono   = 48000
	opusBitrateStereo = 96000
	maxOpusPacketSize = 1500

	// duration of an AU, in units of the clock rate of the format.
	// With HE-AAC the clock rate is the one of the core, so this holds too.
	aacSamplesPerAU = 1024

	// an AU whose PTS is further than this from the expected one is treated
	// as a discontinuity: buffered audio is dropped and timestamps are re-anchored.
	discontinuityTolerance = 100 * time.Millisecond

	// caps the error logs of a stream that keeps failing
	errorLogInterval = 10 * time.Second
)

// Supported returns the channel count of the Opus output, or false if the format
// can't be transcoded by this build. AAC-LC and HE-AAC (v1, v2) with one or two
// channels are supported; multichannel layouts are rare in live streams.
func Supported(forma *format.MPEG4Audio) (int, bool) {
	if !transcodingAvailable {
		return 0, false
	}

	conf := forma.Config
	if conf == nil || conf.SampleRate <= 0 || conf.FrameLengthFlag {
		return 0, false
	}

	switch conf.Type {
	case mpeg4audio.ObjectTypeAACLC, mpeg4audio.ObjectTypeSBR, mpeg4audio.ObjectTypePS:
	default:
		return 0, false
	}

	switch {
	case conf.ChannelConfig == 2, conf.Type == mpeg4audio.ObjectTypePS:
		// parametric stereo decodes a mono core into stereo
		return 2, true
	case conf.ChannelConfig == 1:
		return 1, true
	default:
		return 0, false
	}
}

func isOpusSampleRate(rate int) bool {
	switch rate {
	case 8000, 12000, 16000, 24000, 48000:
		return true
	}
	return false
}

type registryKey struct {
	strm  *stream.Stream
	forma *format.MPEG4Audio
}

var (
	registryMutex sync.Mutex
	registry      = map[registryKey]*transcoder{}
)

// Subscribe attaches onData to the transcoder of the given stream and format,
// starting the transcoder if it's the first subscriber.
// onData receives units containing a single RTP packet with an Opus frame,
// whose timestamp uses a 48kHz clock. It is called from the goroutine of the
// transcoder, never concurrently with itself, and never after the returned
// unsubscribe function returns.
func Subscribe(
	strm *stream.Stream,
	media *description.Media,
	forma *format.MPEG4Audio,
	onData stream.OnDataFunc,
) (unsubscribe func(), err error) {
	key := registryKey{strm: strm, forma: forma}

	registryMutex.Lock()
	defer registryMutex.Unlock()

	t, ok := registry[key]
	if !ok {
		t, err = newTranscoder(strm, media, forma)
		if err != nil {
			return nil, err
		}
		registry[key] = t
	}

	sub := &subscriber{onData: onData}
	t.addSubscriber(sub)

	return func() {
		registryMutex.Lock()
		isLast := t.removeSubscriber(sub)
		if isLast {
			delete(registry, key)
		}
		registryMutex.Unlock()

		if isLast {
			t.close()
		}
	}, nil
}

type subscriber struct {
	onData stream.OnDataFunc
}

type transcoder struct {
	strm   *stream.Stream
	reader *stream.Reader
	inRate int // clock rate of input timestamps

	decoder *aacDecoder

	// set up from the first decoded frame, since with HE-AAC the output
	// sample rate and channel count are known only after decoding
	encoder      *opusEncoder
	resampler    *resampler // nil when the decoded rate is supported by Opus
	decRate      int
	decChannels  int
	frameLen     int // interleaved samples per Opus frame
	frameTicks   int64
	lastAUFrames int // samples per channel of the last decoded AU

	// pending PCM at the encoder rate, waiting to fill an Opus frame
	pcm     []float32
	silence []float32
	packet  []byte

	isAnchored   bool
	nextInPTS    int64 // expected PTS of the next AU, in input clock units
	outPTS       int64 // 48kHz timestamp of the next Opus frame
	anchorOutPTS int64
	anchorNTP    time.Time
	sequence     uint16
	lastErrLog   time.Time

	subsMutex sync.Mutex
	subs      []*subscriber
}

func newTranscoder(
	strm *stream.Stream,
	media *description.Media,
	forma *format.MPEG4Audio,
) (*transcoder, error) {
	if _, ok := Supported(forma); !ok {
		return nil, fmt.Errorf("unsupported MPEG-4 Audio configuration")
	}

	asc, err := forma.Config.Marshal()
	if err != nil {
		return nil, err
	}

	dec, err := newAACDecoder(asc)
	if err != nil {
		return nil, err
	}

	t := &transcoder{
		strm:    strm,
		inRate:  forma.ClockRate(),
		decoder: dec,
		packet:  make([]byte, maxOpusPacketSize),
	}

	t.reader = &stream.Reader{
		// nothing is sent anywhere by this reader, readers of the output count bytes
		SkipOutboundBytes: true,
		Parent:            t,
	}
	t.reader.OnData(media, forma, t.onData)

	strm.AddReader(t.reader)

	t.Log(logger.Info, "started")

	return t, nil
}

// Log implements logger.Writer.
func (t *transcoder) Log(level logger.Level, format string, args ...any) {
	t.strm.Parent.Log(level, "[AAC to Opus] "+format, args...)
}

func (t *transcoder) logErrorRateLimited(format string, args ...any) {
	if now := time.Now(); now.Sub(t.lastErrLog) >= errorLogInterval {
		t.lastErrLog = now
		t.Log(logger.Warn, format, args...)
	}
}

func (t *transcoder) close() {
	// waits for the reader goroutine to exit, so codecs are no longer in use
	t.strm.RemoveReader(t.reader)

	t.decoder.close()
	if t.encoder != nil {
		t.encoder.close()
	}

	t.Log(logger.Info, "stopped")
}

func (t *transcoder) addSubscriber(sub *subscriber) {
	t.subsMutex.Lock()
	t.subs = append(t.subs, sub)
	t.subsMutex.Unlock()
}

// removeSubscriber returns true when no subscribers are left.
// Since emit() holds subsMutex while calling subscribers, no callback of sub
// is running or will run once this returns.
func (t *transcoder) removeSubscriber(sub *subscriber) bool {
	t.subsMutex.Lock()
	defer t.subsMutex.Unlock()

	for i, s := range t.subs {
		if s == sub {
			t.subs = append(t.subs[:i], t.subs[i+1:]...)
			break
		}
	}

	return len(t.subs) == 0
}

func (t *transcoder) onData(u *unit.Unit) error {
	if u.NilPayload() {
		return nil
	}

	for i, au := range u.Payload.(unit.PayloadMPEG4Audio) {
		auPTS := u.PTS + int64(i)*aacSamplesPerAU
		auNTP := u.NTP.Add(ticksToDuration(int64(i)*aacSamplesPerAU, t.inRate))
		t.processAU(au, auPTS, auNTP)
	}

	return nil
}

func (t *transcoder) processAU(au []byte, pts int64, ntp time.Time) {
	tolerance := int64(discontinuityTolerance) * int64(t.inRate) / int64(time.Second)
	if !t.isAnchored || abs(pts-t.nextInPTS) > tolerance {
		t.anchor(pts, ntp)
	}
	t.nextInPTS = pts + aacSamplesPerAU

	samples, rate, channels, err := t.decoder.decode(au)

	switch {
	case err != nil:
		if t.encoder == nil {
			return
		}
		// keep audio in sync with video by replacing the AU with silence
		t.logErrorRateLimited("unable to decode AU, replacing it with silence: %v", err)
		if len(t.silence) != t.lastAUFrames*t.decChannels {
			t.silence = make([]float32, t.lastAUFrames*t.decChannels)
		}
		samples = t.silence

	case len(samples) == 0:
		return

	default:
		if t.encoder == nil || rate != t.decRate || channels != t.decChannels {
			err = t.setupEncoder(rate, channels)
			if err != nil {
				t.logErrorRateLimited("%v", err)
				return
			}
		}
		t.lastAUFrames = len(samples) / channels
	}

	if t.resampler != nil {
		t.pcm = t.resampler.process(samples, t.pcm)
	} else {
		t.pcm = append(t.pcm, samples...)
	}

	consumed := 0
	for len(t.pcm)-consumed >= t.frameLen {
		t.encodeFrame(t.pcm[consumed : consumed+t.frameLen])
		consumed += t.frameLen
	}

	// move the remainder to the front, so the buffer never grows past ~2 frames
	if consumed > 0 {
		n := copy(t.pcm, t.pcm[consumed:])
		t.pcm = t.pcm[:n]
	}
}

func (t *transcoder) setupEncoder(rate int, channels int) error {
	if t.encoder != nil {
		t.encoder.close()
		t.encoder = nil
		t.Log(logger.Info, "decoded audio format changed, restarting encoder")
	}

	encRate := rate
	t.resampler = nil
	if !isOpusSampleRate(rate) {
		encRate = opusClockRate
		t.resampler = newResampler(rate, encRate, channels)
	}

	bitrate := opusBitrateStereo
	if channels == 1 {
		bitrate = opusBitrateMono
	}

	enc, err := newOpusEncoder(encRate, channels, bitrate, opusComplexity)
	if err != nil {
		return err
	}

	t.encoder = enc
	t.decRate = rate
	t.decChannels = channels
	t.frameLen = int(int64(encRate)*int64(opusFrameDuration)/int64(time.Second)) * channels
	t.frameTicks = int64(opusClockRate) * int64(opusFrameDuration) / int64(time.Second)
	t.pcm = t.pcm[:0]

	t.Log(logger.Info, "transcoding %d Hz, %d ch to Opus %d Hz", rate, channels, encRate)

	return nil
}

// anchor restarts the output timeline at the given input PTS, dropping any
// audio buffered from before the discontinuity.
func (t *transcoder) anchor(pts int64, ntp time.Time) {
	t.isAnchored = true
	t.pcm = t.pcm[:0]
	if t.resampler != nil {
		t.resampler.reset()
	}

	t.outPTS = pts * opusClockRate / int64(t.inRate)
	t.anchorOutPTS = t.outPTS
	t.anchorNTP = ntp
}

func (t *transcoder) encodeFrame(pcm []float32) {
	n, err := t.encoder.encode(pcm, t.packet)

	pts := t.outPTS
	t.outPTS += t.frameTicks

	if err != nil {
		t.logErrorRateLimited("unable to encode Opus frame: %v", err)
		return
	}

	t.emit(t.packet[:n], pts)
}

func (t *transcoder) emit(payload []byte, pts int64) {
	t.sequence++

	u := &unit.Unit{
		PTS: pts,
		NTP: t.anchorNTP.Add(ticksToDuration(pts-t.anchorOutPTS, opusClockRate)),
		RTPPackets: []*rtp.Packet{{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: t.sequence,
				Timestamp:      uint32(pts),
			},
			// the encoder output buffer is reused, while WebRTC interceptors
			// can keep packets around for retransmission
			Payload: append([]byte(nil), payload...),
		}},
	}

	t.subsMutex.Lock()
	defer t.subsMutex.Unlock()

	for _, sub := range t.subs {
		sub.onData(u) //nolint:errcheck
	}
}

func ticksToDuration(ticks int64, clockRate int) time.Duration {
	secs := ticks / int64(clockRate)
	rem := ticks % int64(clockRate)
	return time.Duration(secs)*time.Second + time.Duration(rem)*time.Second/time.Duration(clockRate)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
