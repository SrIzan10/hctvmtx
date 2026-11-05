//go:build cgo && aacopus

package aacopus

/*
#cgo pkg-config: libavcodec libavutil opus
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <libavcodec/avcodec.h>
#include <libavutil/channel_layout.h>
#include <libavutil/error.h>
#include <libavutil/frame.h>
#include <libavutil/mem.h>
#include <opus.h>

typedef struct {
	AVCodecContext *ctx;
	AVPacket *pkt;
	AVFrame *frame;
	uint8_t *buf;
	int buf_cap;
} hctv_aac_dec;

static void hctv_aac_dec_free(hctv_aac_dec *d) {
	if (d == NULL) {
		return;
	}
	avcodec_free_context(&d->ctx);
	av_packet_free(&d->pkt);
	av_frame_free(&d->frame);
	av_free(d->buf);
	free(d);
}

static hctv_aac_dec *hctv_aac_dec_new(const uint8_t *asc, int asc_len, char *err, int err_len) {
	const AVCodec *codec = avcodec_find_decoder(AV_CODEC_ID_AAC);
	if (codec == NULL) {
		snprintf(err, err_len, "libavcodec was built without an AAC decoder");
		return NULL;
	}

	hctv_aac_dec *d = calloc(1, sizeof(hctv_aac_dec));
	d->ctx = avcodec_alloc_context3(codec);
	d->pkt = av_packet_alloc();
	d->frame = av_frame_alloc();
	if (d->ctx == NULL || d->pkt == NULL || d->frame == NULL) {
		snprintf(err, err_len, "out of memory");
		hctv_aac_dec_free(d);
		return NULL;
	}

	d->ctx->extradata = av_mallocz(asc_len + AV_INPUT_BUFFER_PADDING_SIZE);
	memcpy(d->ctx->extradata, asc, asc_len);
	d->ctx->extradata_size = asc_len;
	// frames are decoded one at a time by the transcoder goroutine,
	// so decoder threads would only add latency and memory
	d->ctx->thread_count = 1;

	int ret = avcodec_open2(d->ctx, codec, NULL);
	if (ret < 0) {
		av_strerror(ret, err, err_len);
		hctv_aac_dec_free(d);
		return NULL;
	}

	return d;
}

// decodes an access unit and writes interleaved float samples to out, keeping at most
// max_ch channels. Returns the number of samples per channel, 0 when the decoder
// didn't output a frame, or a negative AVERROR.
static int hctv_aac_dec_decode(hctv_aac_dec *d, const uint8_t *au, int len,
		float *out, int out_cap, int max_ch, int *rate, int *channels) {
	// the decoder reads past the end of the input, which must be zero-padded
	int needed = len + AV_INPUT_BUFFER_PADDING_SIZE;
	if (needed > d->buf_cap) {
		uint8_t *buf = av_realloc(d->buf, needed);
		if (buf == NULL) {
			return AVERROR(ENOMEM);
		}
		d->buf = buf;
		d->buf_cap = needed;
	}
	memcpy(d->buf, au, len);
	memset(d->buf + len, 0, AV_INPUT_BUFFER_PADDING_SIZE);

	d->pkt->data = d->buf;
	d->pkt->size = len;

	int ret = avcodec_send_packet(d->ctx, d->pkt);
	if (ret < 0) {
		return ret;
	}

	ret = avcodec_receive_frame(d->ctx, d->frame);
	if (ret == AVERROR(EAGAIN)) {
		return 0;
	}
	if (ret < 0) {
		return ret;
	}

	AVFrame *f = d->frame;
	int n = f->nb_samples;
	int ch = f->ch_layout.nb_channels;
	int och = ch < max_ch ? ch : max_ch;

	if (n * och > out_cap) {
		av_frame_unref(f);
		return AVERROR(ERANGE);
	}

	switch (f->format) {
	case AV_SAMPLE_FMT_FLTP:
		for (int c = 0; c < och; c++) {
			const float *src = (const float *)f->extended_data[c];
			for (int i = 0; i < n; i++) {
				out[i * och + c] = src[i];
			}
		}
		break;

	case AV_SAMPLE_FMT_FLT: {
		const float *src = (const float *)f->extended_data[0];
		for (int i = 0; i < n; i++) {
			for (int c = 0; c < och; c++) {
				out[i * och + c] = src[i * ch + c];
			}
		}
		break;
	}

	case AV_SAMPLE_FMT_S16P:
		for (int c = 0; c < och; c++) {
			const int16_t *src = (const int16_t *)f->extended_data[c];
			for (int i = 0; i < n; i++) {
				out[i * och + c] = src[i] / 32768.0f;
			}
		}
		break;

	default:
		av_frame_unref(f);
		return AVERROR(ENOSYS);
	}

	*rate = f->sample_rate;
	*channels = och;
	av_frame_unref(f);
	return n;
}

// opus_encoder_ctl() is variadic and can't be called from Go directly
static OpusEncoder *hctv_opus_enc_new(int rate, int channels, int bitrate, int complexity, int *err) {
	OpusEncoder *e = opus_encoder_create(rate, channels, OPUS_APPLICATION_RESTRICTED_LOWDELAY, err);
	if (*err != OPUS_OK) {
		return NULL;
	}

	*err = opus_encoder_ctl(e, OPUS_SET_BITRATE(bitrate));
	if (*err == OPUS_OK) {
		*err = opus_encoder_ctl(e, OPUS_SET_COMPLEXITY(complexity));
	}
	if (*err != OPUS_OK) {
		opus_encoder_destroy(e);
		return NULL;
	}

	return e;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

const transcodingAvailable = true

// largest AAC frame: 2048 samples (HE-AAC) of 2 channels, with headroom
const maxDecodedSamples = 2048 * 2 * 2

func avError(code C.int) error {
	buf := make([]C.char, 128)
	C.av_strerror(code, &buf[0], C.size_t(len(buf)))
	return fmt.Errorf("%s", C.GoString(&buf[0]))
}

type aacDecoder struct {
	d   *C.hctv_aac_dec
	out []float32
}

func newAACDecoder(asc []byte) (*aacDecoder, error) {
	if len(asc) == 0 {
		return nil, fmt.Errorf("empty AudioSpecificConfig")
	}

	errBuf := make([]C.char, 128)
	d := C.hctv_aac_dec_new((*C.uint8_t)(unsafe.Pointer(&asc[0])), C.int(len(asc)),
		&errBuf[0], C.int(len(errBuf)))
	if d == nil {
		return nil, fmt.Errorf("unable to open AAC decoder: %s", C.GoString(&errBuf[0]))
	}

	return &aacDecoder{
		d:   d,
		out: make([]float32, maxDecodedSamples),
	}, nil
}

// decode returns interleaved samples with at most 2 channels. The returned slice
// is only valid until the next call.
func (d *aacDecoder) decode(au []byte) ([]float32, int, int, error) {
	if len(au) == 0 {
		return nil, 0, 0, fmt.Errorf("empty access unit")
	}

	var rate, channels C.int
	n := C.hctv_aac_dec_decode(d.d,
		(*C.uint8_t)(unsafe.Pointer(&au[0])), C.int(len(au)),
		(*C.float)(unsafe.Pointer(&d.out[0])), C.int(len(d.out)),
		2, &rate, &channels)
	if n < 0 {
		return nil, 0, 0, avError(n)
	}
	if n == 0 {
		return nil, 0, 0, nil
	}

	return d.out[:int(n)*int(channels)], int(rate), int(channels), nil
}

func (d *aacDecoder) close() {
	C.hctv_aac_dec_free(d.d)
	d.d = nil
}

type opusEncoder struct {
	e        *C.OpusEncoder
	channels int
}

func newOpusEncoder(rate int, channels int, bitrate int, complexity int) (*opusEncoder, error) {
	var errCode C.int
	e := C.hctv_opus_enc_new(C.int(rate), C.int(channels), C.int(bitrate), C.int(complexity), &errCode)
	if e == nil {
		return nil, fmt.Errorf("unable to create Opus encoder: %s", C.GoString(C.opus_strerror(errCode)))
	}

	return &opusEncoder{e: e, channels: channels}, nil
}

// encode encodes one frame of interleaved samples into out and returns the packet size.
func (e *opusEncoder) encode(pcm []float32, out []byte) (int, error) {
	n := C.opus_encode_float(e.e,
		(*C.float)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)/e.channels),
		(*C.uchar)(unsafe.Pointer(&out[0])), C.opus_int32(len(out)))
	if n < 0 {
		return 0, fmt.Errorf("%s", C.GoString(C.opus_strerror(n)))
	}
	return int(n), nil
}

func (e *opusEncoder) close() {
	C.opus_encoder_destroy(e.e)
	e.e = nil
}

// opusDecoder is used by tests to check the encoded output
// (cgo can't be used in test files).
type opusDecoder struct {
	d        *C.OpusDecoder
	channels int
	out      []float32
}

func newOpusDecoder(channels int) (*opusDecoder, error) {
	var errCode C.int
	d := C.opus_decoder_create(opusClockRate, C.int(channels), &errCode)
	if errCode != C.OPUS_OK {
		return nil, fmt.Errorf("unable to create Opus decoder: %s", C.GoString(C.opus_strerror(errCode)))
	}
	return &opusDecoder{d: d, channels: channels, out: make([]float32, 5760*channels)}, nil
}

func (d *opusDecoder) decode(pkt []byte) ([]float32, error) {
	n := C.opus_decode_float(d.d,
		(*C.uchar)(unsafe.Pointer(&pkt[0])), C.opus_int32(len(pkt)),
		(*C.float)(unsafe.Pointer(&d.out[0])), C.int(len(d.out)/d.channels), 0)
	if n < 0 {
		return nil, fmt.Errorf("%s", C.GoString(C.opus_strerror(n)))
	}
	return d.out[:int(n)*d.channels], nil
}

func (d *opusDecoder) close() {
	C.opus_decoder_destroy(d.d)
}
