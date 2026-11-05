package aacopus

import (
	"math"
)

const (
	// taps per polyphase branch. 24 keeps aliasing well below what the Opus encoder
	// can represent at the bitrates we use, for ~2.3M multiply-adds per second of
	// 48kHz stereo output.
	resamplerTaps = 24
	// fraction of the lower Nyquist frequency kept by the anti-aliasing filter.
	resamplerPassband   = 0.92
	resamplerKaiserBeta = 8.0
)

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// besselI0 is the zeroth order modified Bessel function of the first kind,
// used by the Kaiser window.
func besselI0(x float64) float64 {
	sum := 1.0
	term := 1.0
	for k := 1; k < 50; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < sum*1e-12 {
			break
		}
	}
	return sum
}

// resampler is a fixed-ratio polyphase FIR resampler for interleaved float32 PCM.
// The output rate is inRate*l/m. Coefficients are computed once, so per-sample
// cost is resamplerTaps multiply-adds per channel and there are no allocations
// once the internal buffer has grown to its steady-state size.
type resampler struct {
	l        int
	m        int
	channels int
	coefs    []float32 // [phase][tap]

	buf []float32 // interleaved input, starting with resamplerTaps-1 frames of history
	pos int       // position of the next output sample, in units of 1/l input frames
}

func newResampler(inRate int, outRate int, channels int) *resampler {
	g := gcd(inRate, outRate)
	l := outRate / g
	m := inRate / g

	// prototype low-pass filter at the upsampled rate inRate*l
	n := resamplerTaps * l
	cutoff := resamplerPassband * 0.5 * float64(min(inRate, outRate)) / float64(inRate*l)
	center := float64(n-1) / 2
	i0Beta := besselI0(resamplerKaiserBeta)

	proto := make([]float64, n)
	for i := range proto {
		x := float64(i) - center
		var sinc float64
		if x == 0 {
			sinc = 2 * cutoff
		} else {
			sinc = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		r := x / center
		window := besselI0(resamplerKaiserBeta*math.Sqrt(max(0, 1-r*r))) / i0Beta
		// the gain of l compensates for the zeros inserted by upsampling
		proto[i] = sinc * window * float64(l)
	}

	coefs := make([]float32, l*resamplerTaps)
	for phase := range l {
		for k := range resamplerTaps {
			coefs[phase*resamplerTaps+k] = float32(proto[phase+k*l])
		}
	}

	r := &resampler{
		l:        l,
		m:        m,
		channels: channels,
		coefs:    coefs,
	}
	r.reset()
	return r
}

// reset drops buffered input, used on stream discontinuities.
func (r *resampler) reset() {
	r.buf = make([]float32, (resamplerTaps-1)*r.channels, 4096)
	r.pos = (resamplerTaps - 1) * r.l
}

// process resamples in and appends the result to out.
func (r *resampler) process(in []float32, out []float32) []float32 {
	r.buf = append(r.buf, in...)
	ch := r.channels
	frames := len(r.buf) / ch

	for {
		base := r.pos / r.l
		if base >= frames {
			break
		}
		phaseCoefs := r.coefs[(r.pos%r.l)*resamplerTaps : (r.pos%r.l+1)*resamplerTaps]

		for c := range ch {
			var acc float32
			idx := base*ch + c
			for _, coef := range phaseCoefs {
				acc += coef * r.buf[idx]
				idx -= ch
			}
			out = append(out, acc)
		}

		r.pos += r.m
	}

	// keep only the history needed by the next call
	// (when downsampling, pos can already point past the end of the buffer)
	drop := min(r.pos/r.l, frames) - (resamplerTaps - 1)
	if drop > 0 {
		n := copy(r.buf, r.buf[drop*ch:])
		r.buf = r.buf[:n]
		r.pos -= drop * r.l
	}

	return out
}
