package gohlslib

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Covers the bounded DTS-error tolerance in muxerSegmenter (see
// maxConsecutiveDTSErrors in muxer_segmenter.go): access units whose DTS
// cannot be extracted are discarded without interrupting the muxer, the
// counter resets on every successful extraction, and only a run of
// maxConsecutiveDTSErrors consecutive failures propagates the error.

func createDTSToleranceMuxer(t *testing.T, variant MuxerVariant) *Muxer {
	m := &Muxer{
		Variant:            variant,
		SegmentCount:       3,
		SegmentMinDuration: 1 * time.Second,
		Tracks:             []*Track{testVideoTrack},
	}
	err := m.Start()
	require.NoError(t, err)
	return m
}

func writeTestIDR(m *Muxer, pts int64) error {
	return m.WriteH264(testVideoTrack, testTime, pts, [][]byte{
		testSPS, // SPS
		{8},     // PPS
		{5},     // IDR
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
		t.Run(ca.name, func(t *testing.T) {
			m := createDTSToleranceMuxer(t, ca.variant)
			defer m.Close()

			require.NoError(t, writeTestIDR(m, 90000))
			require.NoError(t, writeTestIDR(m, 180000))

			// non-monotonic DTS: must be discarded without an error
			require.NoError(t, writeTestIDR(m, 90000))

			// the stream keeps working afterwards
			require.NoError(t, writeTestIDR(m, 270000))
		})
	}
}

func TestMuxerDTSErrorGiveUp(t *testing.T) {
	m := createDTSToleranceMuxer(t, MuxerVariantFMP4)
	defer m.Close()

	require.NoError(t, writeTestIDR(m, 900000))

	// the first maxConsecutiveDTSErrors-1 failures are discarded silently
	for i := 0; i < maxConsecutiveDTSErrors-1; i++ {
		require.NoError(t, writeTestIDR(m, int64(1000+i)))
	}

	// the boundary failure propagates the error
	require.Error(t, writeTestIDR(m, 500))
}

func TestMuxerDTSErrorCounterResetsOnSuccess(t *testing.T) {
	m := createDTSToleranceMuxer(t, MuxerVariantFMP4)
	defer m.Close()

	require.NoError(t, writeTestIDR(m, 900000))

	// one failure short of the limit...
	for i := 0; i < maxConsecutiveDTSErrors-1; i++ {
		require.NoError(t, writeTestIDR(m, int64(1000+i)))
	}

	// ...a successful unit resets the counter...
	require.NoError(t, writeTestIDR(m, 990000))

	// ...so a fresh run below the limit is discarded again without error
	for i := 0; i < maxConsecutiveDTSErrors-1; i++ {
		require.NoError(t, writeTestIDR(m, int64(2000+i)))
	}

	// and the muxer still accepts good input
	require.NoError(t, writeTestIDR(m, 1080000))
}
