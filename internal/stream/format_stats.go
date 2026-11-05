package stream

import (
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/vp8"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/vp9"

	"github.com/bluenviron/mediamtx/internal/unit"
)

const (
	// frame rate and bitrate are averaged over windows of this length
	formatStatsWindow = 5 * time.Second
	// the reported keyframe interval is the largest of the last intervals,
	// so that a single early keyframe (scene cut) doesn't hide a long interval
	formatStatsKeyframeHistory = 4
)

// FormatStats contains statistics about the media written to a format,
// measured on what publishers send. They describe encoder settings that
// affect latency and compatibility, like the keyframe interval and B-frames.
type FormatStats struct {
	// average frame rate over the last window. Zero for audio or until measured.
	FrameRate float64
	// average bitrate over the last window, in bits per second. Zero until measured.
	Bitrate uint64
	// largest of the most recent intervals between keyframes. Zero for audio or until measured.
	KeyframeInterval time.Duration
	// whether frames have been received out of presentation order,
	// which happens when the encoder uses B-frames.
	HasBFrames bool
}

// formatStats collects FormatStats. Updates happen on the write path of the stream,
// so they're limited to a few integer operations per unit.
type formatStats struct {
	forma      format.Format
	clockRate  int64
	isVideo    bool
	isKeyframe func(unit.Payload) bool

	mutex sync.Mutex

	hasPrevPTS bool
	prevPTS    int64

	windowStarted bool
	windowStart   int64
	windowFrames  uint64
	windowBytes   uint64

	hasLastKeyframe bool
	lastKeyframePTS int64
	keyframeHistory [formatStatsKeyframeHistory]int64
	keyframeCount   int

	out FormatStats
}

func newFormatStats(forma format.Format) *formatStats {
	s := &formatStats{
		forma: forma,
	}

	switch forma.(type) {
	case *format.H264:
		s.isVideo = true
		s.isKeyframe = func(p unit.Payload) bool { return h264.IsRandomAccess(p.(unit.PayloadH264)) }
	case *format.H265:
		s.isVideo = true
		s.isKeyframe = func(p unit.Payload) bool { return h265.IsRandomAccess(p.(unit.PayloadH265)) }
	case *format.AV1:
		s.isVideo = true
		s.isKeyframe = func(p unit.Payload) bool { return av1.IsRandomAccess2(p.(unit.PayloadAV1)) }
	case *format.VP9:
		s.isVideo = true
		s.isKeyframe = func(p unit.Payload) bool { return vp9.IsRandomAccess(p.(unit.PayloadVP9)) }
	case *format.VP8:
		s.isVideo = true
		s.isKeyframe = func(p unit.Payload) bool { return vp8.IsRandomAccess(p.(unit.PayloadVP8)) }
	}

	return s
}

// safeClockRate returns the clock rate of a format, or zero if it can't be
// computed yet (some formats need parameters that are received later, like
// MPEG-4 Audio without a config).
func safeClockRate(forma format.Format) (rate int64) {
	defer func() {
		if recover() != nil {
			rate = 0
		}
	}()
	return int64(forma.ClockRate())
}

func (s *formatStats) update(u *unit.Unit, size uint64) {
	if u.NilPayload() {
		return
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.clockRate <= 0 {
		s.clockRate = safeClockRate(s.forma)
		if s.clockRate <= 0 {
			return
		}
	}

	pts := u.PTS
	window := int64(formatStatsWindow) * s.clockRate / int64(time.Second)

	// units are written in decoding order, so a PTS lower than the previous one
	// means frames are reordered, which only B-frames do
	if s.isVideo && s.hasPrevPTS && pts < s.prevPTS {
		s.out.HasBFrames = true
	}
	s.hasPrevPTS = true
	s.prevPTS = pts

	if s.isKeyframe != nil && s.isKeyframe(u.Payload) {
		if s.hasLastKeyframe && pts > s.lastKeyframePTS {
			s.keyframeHistory[s.keyframeCount%formatStatsKeyframeHistory] = pts - s.lastKeyframePTS
			s.keyframeCount++

			var maxInterval int64
			for i := range min(s.keyframeCount, formatStatsKeyframeHistory) {
				maxInterval = max(maxInterval, s.keyframeHistory[i])
			}
			s.out.KeyframeInterval = time.Duration(maxInterval) * time.Second / time.Duration(s.clockRate)
		}
		s.hasLastKeyframe = true
		s.lastKeyframePTS = pts
	}

	// restart on discontinuities, but not on the small steps back caused by B-frames
	if !s.windowStarted || pts < s.windowStart-s.clockRate {
		s.windowStarted = true
		s.windowStart = pts
		s.windowFrames = 0
		s.windowBytes = 0
	}

	s.windowFrames++
	s.windowBytes += size

	if elapsed := pts - s.windowStart; elapsed >= window {
		seconds := float64(elapsed) / float64(s.clockRate)
		if s.isVideo {
			s.out.FrameRate = float64(s.windowFrames) / seconds
		}
		s.out.Bitrate = uint64(float64(s.windowBytes*8) / seconds)

		s.windowStart = pts
		s.windowFrames = 0
		s.windowBytes = 0
	}
}

func (s *formatStats) get() FormatStats {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.out
}
