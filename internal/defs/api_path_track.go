package defs

import (
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/bluenviron/mediamtx/internal/formatlabel"
)

// APIPathTrack is a track.
type APIPathTrack struct {
	Codec      APIPathTrackCodec      `json:"codec"`
	CodecProps APIPathTrackCodecProps `json:"codecProps"`
	Stats      *APIPathTrackStats     `json:"stats"`
}

// APIPathTrackStats contains statistics of a track, measured on what the publisher sends.
type APIPathTrackStats struct {
	// average frames per second. Zero for audio tracks or until measured.
	FrameRate float64 `json:"frameRate"`
	// average bits per second. Zero until measured.
	Bitrate uint64 `json:"bitrate"`
	// seconds between keyframes. Zero for audio tracks or until measured.
	KeyframeInterval float64 `json:"keyframeInterval"`
	// whether the track contains B-frames.
	HasBFrames bool `json:"hasBFrames"`
}

func formatToTrack(outFormat format.Format) APIPathTrack {
	return APIPathTrack{
		Codec:      formatlabel.FormatToLabel(outFormat),
		CodecProps: formatToTrackCodecProps(outFormat),
	}
}

func gatherFormats(medias []*description.Media) []format.Format {
	n := 0
	for _, media := range medias {
		n += len(media.Formats)
	}

	if n == 0 {
		return nil
	}

	formats := make([]format.Format, n)
	n = 0

	for _, media := range medias {
		n += copy(formats[n:], media.Formats)
	}

	return formats
}

func formatsToTracks(outFormats []format.Format) []APIPathTrack {
	ret := make([]APIPathTrack, len(outFormats))

	for i, outFormat := range outFormats {
		ret[i] = formatToTrack(outFormat)
	}

	return ret
}

// MediasToTracks returns tracks of given medias.
func MediasToTracks(outMedias []*description.Media) []APIPathTrack {
	return formatsToTracks(gatherFormats(outMedias))
}

// MediasToTracksWithStats returns tracks of given medias, with statistics
// given in the same order as formats of medias.
func MediasToTracksWithStats(outMedias []*description.Media, stats []APIPathTrackStats) []APIPathTrack {
	tracks := MediasToTracks(outMedias)
	for i := range tracks {
		if i < len(stats) {
			tracks[i].Stats = &stats[i]
		}
	}
	return tracks
}
