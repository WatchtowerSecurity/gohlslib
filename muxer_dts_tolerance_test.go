package gohlslib

import (
	"testing"
	"time"

	"github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/stretchr/testify/require"
)

// Covers the bounded DTS-error tolerance in muxerSegmenter (see
// maxDTSErrorBudget in muxer_segmenter.go): access units whose DTS
// cannot be extracted are discarded without interrupting the muxer, the
// per-track failure counter decays on every successful extraction, and only
// a counter reaching the budget propagates the error.

var dtsTestTime = time.Date(2010, 0o1, 0o1, 0o1, 0o1, 0o1, 0, time.UTC)

// baseline profile without POC
var dtsTestH264SPS = []byte{
	0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02,
	0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04,
	0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9,
	0x20,
}

var testDTSVideoTrackH264 = &Track{
	Codec: &codecs.H264{
		SPS: dtsTestH264SPS,
		PPS: []byte{0x08},
	},
	ClockRate: 90000,
}

var testDTSVideoTrackH265 = &Track{
	Codec: &codecs.H265{
		VPS: []byte{
			0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60,
			0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03,
			0x00, 0x00, 0x03, 0x00, 0x78, 0x99, 0x98, 0x09,
		},
		SPS: []byte{
			0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03,
			0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03,
			0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5,
			0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00,
			0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
			0xe0, 0x80,
		},
		PPS: []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40},
	},
	ClockRate: 90000,
}

func createDTSToleranceMuxer(t *testing.T, variant MuxerVariant, codec string) *Muxer {
	track := testDTSVideoTrackH264
	if codec == "h265" {
		track = testDTSVideoTrackH265
	}
	m := &Muxer{
		Variant:            variant,
		SegmentCount:       3,
		SegmentMinDuration: 1 * time.Second,
		Tracks:             []*Track{track},
	}
	err := m.Start()
	require.NoError(t, err)
	return m
}

func writeTestIDR(m *Muxer, codec string, pts int64) error {
	if codec == "h265" {
		c := testDTSVideoTrackH265.Codec.(*codecs.H265)
		return m.WriteH265(testDTSVideoTrackH265, dtsTestTime, pts, [][]byte{
			c.VPS,
			c.SPS,
			c.PPS,
			{0x26, 0x01, 0xaf, 0x08, 0x42, 0x23, 0x48, 0x8a, 0x43, 0xe2}, // IDR_W_RADL
		})
	}
	return m.WriteH264(testDTSVideoTrackH264, dtsTestTime, pts, [][]byte{
		dtsTestH264SPS, // SPS
		{8},            // PPS
		{5},            // IDR
	})
}

func TestMuxerDTSErrorTolerance(t *testing.T) {
	for _, ca := range []struct {
		name    string
		variant MuxerVariant
	}{
		{"mpegts", MuxerVariantMPEGTS},
		{"fmp4", MuxerVariantFMP4},
	} {
		for _, codec := range []string{"h264", "h265"} {
			if ca.variant == MuxerVariantMPEGTS && codec == "h265" {
				// the MPEG-TS variant only supports H264
				continue
			}
			t.Run(ca.name+"_"+codec, func(t *testing.T) {
				m := createDTSToleranceMuxer(t, ca.variant, codec)
				defer m.Close()

				require.NoError(t, writeTestIDR(m, codec, 90000))
				require.NoError(t, writeTestIDR(m, codec, 180000))

				// non-monotonic DTS: must be discarded without an error
				require.NoError(t, writeTestIDR(m, codec, 90000))

				// the stream keeps working afterwards
				require.NoError(t, writeTestIDR(m, codec, 270000))
			})
		}
	}
}

func TestMuxerDTSErrorGiveUp(t *testing.T) {
	for _, codec := range []string{"h264", "h265"} {
		t.Run(codec, func(t *testing.T) {
			m := createDTSToleranceMuxer(t, MuxerVariantFMP4, codec)
			defer m.Close()

			require.NoError(t, writeTestIDR(m, codec, 900000))

			// the first maxDTSErrorBudget-1 failures are discarded
			for i := range maxDTSErrorBudget - 1 {
				require.NoError(t, writeTestIDR(m, codec, int64(1000+i)))
			}

			// the budget boundary propagates the error
			require.Error(t, writeTestIDR(m, codec, 500))
		})
	}
}

func TestMuxerDTSErrorCounterDecaysOnSuccess(t *testing.T) {
	m := createDTSToleranceMuxer(t, MuxerVariantFMP4, "h264")
	defer m.Close()

	require.NoError(t, writeTestIDR(m, "h264", 900000))

	// one failure short of the budget...
	for i := range maxDTSErrorBudget - 1 {
		require.NoError(t, writeTestIDR(m, "h264", int64(1000+i)))
	}

	// ...a successful unit decays the counter by one...
	require.NoError(t, writeTestIDR(m, "h264", 990000))

	// ...so exactly one more failure is tolerated...
	require.NoError(t, writeTestIDR(m, "h264", 3000))

	// ...and the next one reaches the budget
	require.Error(t, writeTestIDR(m, "h264", 500))
}
