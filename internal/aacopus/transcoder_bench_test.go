//go:build cgo && aacopus

package aacopus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// BenchmarkTranscode measures the CPU cost of transcoding audio, reported as the
// percentage of one core used by a stream that is being transcoded.
func BenchmarkTranscode(b *testing.B) {
	for _, file := range []string{
		"sine1k_48000_stereo.aac",
		"sine1k_44100_mono.aac",
	} {
		b.Run(file, func(b *testing.B) {
			conf, aus := loadADTS(b, "testdata/"+file)
			ts := newTestStream(b, conf)

			asc, err := conf.Marshal()
			require.NoError(b, err)
			dec, err := newAACDecoder(asc)
			require.NoError(b, err)

			// drive the pipeline directly, without the reader goroutine
			t := &transcoder{
				strm:    ts.strm,
				inRate:  conf.SampleRate,
				decoder: dec,
				packet:  make([]byte, maxOpusPacketSize),
			}
			defer func() {
				dec.close()
				if t.encoder != nil {
					t.encoder.close()
				}
			}()

			ntp := time.Now()
			pts := int64(0)

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				for _, au := range aus {
					t.processAU(au, pts, ntp)
					pts += aacSamplesPerAU
				}
			}

			audioSeconds := float64(len(aus)*aacSamplesPerAU) / float64(conf.SampleRate)
			b.ReportMetric(100*b.Elapsed().Seconds()/float64(b.N)/audioSeconds, "%core")
		})
	}
}
