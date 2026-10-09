// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr string
	}{
		{in: "256m", want: 256 << 20},
		{in: "256M", want: 256 << 20},
		{in: "256mb", want: 256 << 20},
		{in: "256 MiB", want: 256 << 20},
		{in: " 512k ", want: 512 << 10},
		{in: "1g", want: 1 << 30},
		{in: "1.5g", want: 1536 << 20},
		{in: "1t", want: 1 << 40},
		// No suffix is already bytes.
		{in: "268435456", want: 268435456},

		{in: "", wantErr: "empty size"},
		{in: "m", wantErr: "has no number"},
		{in: "256q", wantErr: "unknown suffix"},
		{in: "0", wantErr: "must be greater than zero"},
		{in: "0m", wantErr: "must be greater than zero"},
		{in: "-5m", wantErr: "must be greater than zero"},
		// A fraction that does not land on a whole byte is rejected rather than
		// rounded: the caller asked for a size that does not exist.
		{in: "1.5b", wantErr: "not a whole number of bytes"},
		// 2^53 tebibytes is 2^93 bytes, well past int64.
		{in: "9007199254740992t", wantErr: "too large"},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseByteSize(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseByteSize_SuffixesAreBinary pins the 1024 base. A container runtime
// states shm and memory sizes in binary units, so a decimal reading here would
// mean a different size than the same string passed to `podman --shm-size`.
func TestParseByteSize_SuffixesAreBinary(t *testing.T) {
	got, err := ParseByteSize("1kb")
	require.NoError(t, err)
	assert.Equal(t, int64(1024), got, "k means 1024, not 1000")
	assert.NotEqual(t, int64(1000), got)
}

func TestContainerDefShmSizeBytes(t *testing.T) {
	// Undeclared is 0 with no error: it means "leave the runtime default",
	// which is not the same request as "give me zero bytes".
	undeclared, err := ContainerDef{Name: "apps-x"}.ShmSizeBytes()
	require.NoError(t, err)
	assert.Zero(t, undeclared)

	declared, err := ContainerDef{Name: "apps-x", ShmSize: "256m"}.ShmSizeBytes()
	require.NoError(t, err)
	assert.Equal(t, int64(256<<20), declared)

	_, err = ContainerDef{Name: "apps-x", ShmSize: "256mo"}.ShmSizeBytes()
	require.Error(t, err, "a size the parser cannot read is an error, not a silent default")
}
