//go:build cgo && aacopus

package aacopus

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/opus"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/test"
	"github.com/bluenviron/mediamtx/internal/unit"
)

func loadADTS(t testing.TB, path string) (*mpeg4audio.AudioSpecificConfig, [][]byte) {
	buf, err := os.ReadFile(path)
	require.NoError(t, err)

	var pkts mpeg4audio.ADTSPackets
	err = pkts.Unmarshal(buf)
	require.NoError(t, err)

	aus := make([][]byte, len(pkts))
	for i, pkt := range pkts {
		aus[i] = pkt.AU
	}

	return &mpeg4audio.AudioSpecificConfig{
		Type:          pkts[0].Type,
		SampleRate:    pkts[0].SampleRate,
		ChannelConfig: pkts[0].ChannelConfig,
		ChannelCount:  int(pkts[0].ChannelConfig),
	}, aus
}

type testStream struct {
	strm      *stream.Stream
	subStream *stream.SubStream
	media     *description.Media
	forma     *format.MPEG4Audio
}

func newTestStream(t testing.TB, conf *mpeg4audio.AudioSpecificConfig) *testStream {
	forma := &format.MPEG4Audio{
		PayloadTyp:       96,
		Config:           conf,
		SizeLength:       13,
		IndexLength:      3,
		IndexDeltaLength: 3,
	}
	media := &description.Media{
		Type:    description.MediaTypeAudio,
		Formats: []format.Format{forma},
	}

	strm := &stream.Stream{
		OrigDesc:          &description.Session{Medias: []*description.Media{media}},
		WriteQueueSize:    512,
		RTPMaxPayloadSize: 1450,
		Parent:            test.NilLogger,
	}
	err := strm.Initialize()
	require.NoError(t, err)
	t.Cleanup(strm.Close)

	subStream := &stream.SubStream{Stream: strm}
	err = subStream.Initialize()
	require.NoError(t, err)

	return &testStream{strm: strm, subStream: subStream, media: media, forma: forma}
}

func (ts *testStream) writeAU(au []byte, pts int64, ntp time.Time) {
	ts.subStream.WriteUnit(ts.media, ts.forma, &unit.Unit{
		PTS:     pts,
		NTP:     ntp,
		Payload: unit.PayloadMPEG4Audio{au},
	})
}

type collector struct {
	mutex sync.Mutex
	units []*unit.Unit
}

func (c *collector) onData(u *unit.Unit) error {
	c.mutex.Lock()
	c.units = append(c.units, u)
	c.mutex.Unlock()
	return nil
}

func (c *collector) waitFor(t *testing.T, n int) []*unit.Unit {
	require.Eventually(t, func() bool {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		return len(c.units) >= n
	}, 5*time.Second, 5*time.Millisecond)

	c.mutex.Lock()
	defer c.mutex.Unlock()
	return append([]*unit.Unit(nil), c.units...)
}

func registrySize() int {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	return len(registry)
}

func TestSupported(t *testing.T) {
	for _, ca := range []struct {
		name string
		conf *mpeg4audio.AudioSpecificConfig
		ok   bool
	}{
		{"lc stereo", &mpeg4audio.AudioSpecificConfig{
			Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 48000, ChannelConfig: 2,
		}, true},
		{"lc mono 44.1k", &mpeg4audio.AudioSpecificConfig{
			Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 44100, ChannelConfig: 1,
		}, true},
		{"he-aac", &mpeg4audio.AudioSpecificConfig{
			Type: mpeg4audio.ObjectTypeSBR, SampleRate: 24000, ChannelConfig: 2,
			ExtensionType: mpeg4audio.ObjectTypeSBR, ExtensionSampleRate: 48000,
		}, true},
		{"he-aac v2 (parametric stereo)", &mpeg4audio.AudioSpecificConfig{
			Type: mpeg4audio.ObjectTypePS, SampleRate: 24000, ChannelConfig: 1,
			ExtensionType: mpeg4audio.ObjectTypePS, ExtensionSampleRate: 48000,
		}, true},
		{"5.1", &mpeg4audio.AudioSpecificConfig{
			Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 48000, ChannelConfig: 6,
		}, false},
		{"960 samples per frame", &mpeg4audio.AudioSpecificConfig{
			Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 48000, ChannelConfig: 2,
			FrameLengthFlag: true,
		}, false},
	} {
		t.Run(ca.name, func(t *testing.T) {
			_, ok := Supported(&format.MPEG4Audio{Config: ca.conf})
			require.Equal(t, ca.ok, ok)
		})
	}
}

func TestTranscode(t *testing.T) {
	for _, ca := range []struct {
		file     string
		channels int
	}{
		{"testdata/sine1k_48000_stereo.aac", 2},
		{"testdata/sine1k_44100_mono.aac", 1},
	} {
		t.Run(ca.file, func(t *testing.T) {
			conf, aus := loadADTS(t, ca.file)
			ts := newTestStream(t, conf)

			// two readers share a single transcoder
			var c1, c2 collector
			unsub1, err := Subscribe(ts.strm, ts.media, ts.forma, c1.onData)
			require.NoError(t, err)
			unsub2, err := Subscribe(ts.strm, ts.media, ts.forma, c2.onData)
			require.NoError(t, err)
			require.Equal(t, 1, registrySize())

			ntp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			basePTS := int64(123456)
			for i, au := range aus {
				pts := basePTS + int64(i)*aacSamplesPerAU
				ts.writeAU(au, pts, ntp.Add(ticksToDuration(int64(i)*aacSamplesPerAU, conf.SampleRate)))
			}

			// ~1s of audio in 20ms frames, minus what is still buffered
			expected := len(aus)*aacSamplesPerAU*50/conf.SampleRate - 2
			units1 := c1.waitFor(t, expected)
			units2 := c2.waitFor(t, expected)
			require.Equal(t, len(units1), len(units2))

			dec, err := newOpusDecoder(ca.channels)
			require.NoError(t, err)
			defer dec.close()

			firstPTS := basePTS * 48000 / int64(conf.SampleRate)
			var decoded []float32

			for i, u := range units1 {
				require.Same(t, u, units2[i], "readers receive the same output")
				require.Len(t, u.RTPPackets, 1)
				pkt := u.RTPPackets[0]

				require.Equal(t, int64(960), opus.PacketDuration2(pkt.Payload), "20ms frames")
				require.Equal(t, uint32(u.PTS), pkt.Timestamp)
				if i > 0 {
					prev := units1[i-1].RTPPackets[0]
					require.Equal(t, prev.SequenceNumber+1, pkt.SequenceNumber)
					require.Equal(t, prev.Timestamp+960, pkt.Timestamp)
				} else {
					// output is anchored to the input timeline
					require.Equal(t, firstPTS, u.PTS)
				}
				require.Equal(t,
					ntp.Add(ticksToDuration(u.PTS-firstPTS, 48000)), u.NTP, "NTP follows PTS")

				pcm, err2 := dec.decode(pkt.Payload)
				require.NoError(t, err2)
				decoded = append(decoded, pcm...)
			}

			// the 1kHz sine survives AAC -> PCM -> (resampling) -> Opus -> PCM
			steady := decoded[len(decoded)/4 : len(decoded)*3/4]
			require.InDelta(t, 1000, dominantFrequency(steady, ca.channels, 48000), 10)
			require.Greater(t, rms(steady), 0.05)

			// the transcoder stops with its last reader
			unsub1()
			require.Equal(t, 1, registrySize())
			unsub2()
			require.Equal(t, 0, registrySize())
		})
	}
}

func TestTranscodeDiscontinuity(t *testing.T) {
	conf, aus := loadADTS(t, "testdata/sine1k_48000_stereo.aac")
	ts := newTestStream(t, conf)

	var c collector
	unsub, err := Subscribe(ts.strm, ts.media, ts.forma, c.onData)
	require.NoError(t, err)
	defer unsub()

	ntp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	half := len(aus) / 2

	for i, au := range aus[:half] {
		ts.writeAU(au, int64(i)*aacSamplesPerAU, ntp)
	}

	// the publisher jumps 10 seconds ahead (e.g. a reconnecting encoder)
	jump := int64(10 * conf.SampleRate)
	for i, au := range aus[half:] {
		ts.writeAU(au, jump+int64(half+i)*aacSamplesPerAU, ntp)
	}

	units := c.waitFor(t, len(aus)*aacSamplesPerAU/960-4)

	jumps := 0
	for i := 1; i < len(units); i++ {
		delta := units[i].RTPPackets[0].Timestamp - units[i-1].RTPPackets[0].Timestamp
		if delta != 960 {
			jumps++
			// timestamps follow the input instead of drifting by the dropped audio
			require.InDelta(t, 10*48000, int64(delta), 2*960)
		}
	}
	require.Equal(t, 1, jumps)
}
