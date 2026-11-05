package stream

import (
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/unit"
)

var (
	testIDR    = unit.PayloadH264{{0x65, 0x88, 0x84}}
	testNonIDR = unit.PayloadH264{{0x41, 0x9a, 0x02}}
)

// writeH264 writes seconds of 30fps H264 with a keyframe every gop frames.
// With bFrames, frames are written in decoding order IPBBPBB..., like x264 does.
func writeH264(s *formatStats, seconds int, gop int, bFrames bool, scene map[int]bool) {
	const fps = 30
	frameTicks := int64(90000 / fps)

	for i := range seconds * fps {
		pts := int64(i) * frameTicks
		if bFrames && i%gop != 0 {
			// swap pairs of frames after the keyframe: decode order 1,0 -> PTS goes back
			switch i % 2 {
			case 1:
				pts += frameTicks
			case 0:
				pts -= frameTicks
			}
		}

		payload := testNonIDR
		if i%gop == 0 || scene[i] {
			payload = testIDR
		}

		s.update(&unit.Unit{PTS: pts, Payload: payload}, 1000)
	}
}

func TestFormatStatsH264(t *testing.T) {
	s := newFormatStats(&format.H264{PacketizationMode: 1})
	writeH264(s, 12, 60, false, nil)

	st := s.get()
	require.InDelta(t, 30, st.FrameRate, 0.5)
	require.Equal(t, 2*time.Second, st.KeyframeInterval)
	require.False(t, st.HasBFrames)
	require.InDelta(t, 30*1000*8, float64(st.Bitrate), 30*1000*8*0.05)
}

func TestFormatStatsBFrames(t *testing.T) {
	s := newFormatStats(&format.H264{PacketizationMode: 1})
	writeH264(s, 12, 60, true, nil)

	st := s.get()
	require.True(t, st.HasBFrames)
	require.InDelta(t, 30, st.FrameRate, 1)
	require.Equal(t, 2*time.Second, st.KeyframeInterval)
}

func TestFormatStatsKeyframeIntervalIgnoresSceneCuts(t *testing.T) {
	s := newFormatStats(&format.H264{PacketizationMode: 1})
	// a keyframe every 10s, plus a scene cut 1s after the last one
	writeH264(s, 31, 300, false, map[int]bool{630: true})

	require.Equal(t, 10*time.Second, s.get().KeyframeInterval)
}

func TestFormatStatsAudio(t *testing.T) {
	s := newFormatStats(&format.MPEG4Audio{Config: &mpeg4audio.AudioSpecificConfig{
		Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 48000, ChannelConfig: 2,
	}})

	for i := range 48000 * 6 / 1024 {
		s.update(&unit.Unit{
			PTS:     int64(i) * 1024,
			Payload: unit.PayloadMPEG4Audio{{1, 2, 3}},
		}, 400)
	}

	st := s.get()
	require.Zero(t, st.FrameRate)
	require.Zero(t, st.KeyframeInterval)
	require.False(t, st.HasBFrames)
	require.InDelta(t, 400*8*48000/1024, float64(st.Bitrate), 2000)
}
