//go:build !(cgo && aacopus)

package aacopus

import (
	"errors"
)

// transcoding needs libavcodec and libopus, which are linked only when building
// with CGO_ENABLED=1 and the aacopus tag (see docker/transcode.Dockerfile).
const transcodingAvailable = false

var errNotAvailable = errors.New("AAC to Opus transcoding is not included in this build")

type aacDecoder struct{}

func newAACDecoder(_ []byte) (*aacDecoder, error) {
	return nil, errNotAvailable
}

func (*aacDecoder) decode(_ []byte) ([]float32, int, int, error) {
	return nil, 0, 0, errNotAvailable
}

func (*aacDecoder) close() {}

type opusEncoder struct{}

func newOpusEncoder(_ int, _ int, _ int, _ int) (*opusEncoder, error) {
	return nil, errNotAvailable
}

func (*opusEncoder) encode(_ []float32, _ []byte) (int, error) {
	return 0, errNotAvailable
}

func (*opusEncoder) close() {}
