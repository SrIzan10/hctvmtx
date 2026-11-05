//go:build !(cgo && aacopus)

package aacopus

import (
	"testing"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"
)

func TestSupportedWithoutCodecs(t *testing.T) {
	_, ok := Supported(&format.MPEG4Audio{Config: &mpeg4audio.AudioSpecificConfig{
		Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 48000, ChannelConfig: 2,
	}})
	require.False(t, ok, "builds without the codecs must not offer transcoding")
}
