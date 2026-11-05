package aacopus

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func sine(rate int, freq float64, frames int, channels int) []float32 {
	out := make([]float32, frames*channels)
	for i := range frames {
		v := float32(0.5 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
		for c := range channels {
			out[i*channels+c] = v
		}
	}
	return out
}

// dominantFrequency estimates the frequency of a sine from its zero crossings.
func dominantFrequency(samples []float32, channels int, rate int) float64 {
	crossings := 0
	frames := len(samples) / channels
	for i := 1; i < frames; i++ {
		if (samples[(i-1)*channels] < 0) != (samples[i*channels] < 0) {
			crossings++
		}
	}
	return float64(crossings) / 2 / (float64(frames) / float64(rate))
}

func rms(samples []float32) float64 {
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func TestResampler(t *testing.T) {
	for _, ca := range []struct {
		in       int
		out      int
		channels int
	}{
		{44100, 48000, 2},
		{44100, 48000, 1},
		{22050, 48000, 2},
		{32000, 48000, 1},
		{96000, 48000, 2},
	} {
		t.Run("", func(t *testing.T) {
			r := newResampler(ca.in, ca.out, ca.channels)
			input := sine(ca.in, 1000, ca.in, ca.channels) // 1 second

			// feed it in uneven chunks, like AAC frames would
			var out []float32
			for pos := 0; pos < len(input); {
				n := min(1024*ca.channels, len(input)-pos)
				out = r.process(input[pos:pos+n], out)
				pos += n
			}

			frames := len(out) / ca.channels
			require.InDelta(t, ca.out, frames, resamplerTaps, "output length")

			// skip the filter warm-up before measuring
			steady := out[resamplerTaps*4*ca.channels:]
			require.InDelta(t, 1000, dominantFrequency(steady, ca.channels, ca.out), 5)
			require.InDelta(t, 0.5/math.Sqrt2, rms(steady), 0.01, "gain")
		})
	}
}

func TestResamplerRejectsAliases(t *testing.T) {
	// 30kHz is above the 24kHz Nyquist frequency of the output and must be filtered out
	r := newResampler(96000, 48000, 1)
	out := r.process(sine(96000, 30000, 96000, 1), nil)
	require.Less(t, rms(out[resamplerTaps*4:]), 0.01)
}
