// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"math/big"
	"strings"
)

// sizeMultipliers maps a lowercased size suffix to its multiplier. Every suffix
// is 1024-based, the way a container runtime states a shm or memory size:
// podman's `--shm-size 1g` is 1073741824 bytes, not 1000000000. A bare
// number with no suffix is already bytes.
var sizeMultipliers = map[string]int64{
	"":    1,
	"b":   1,
	"k":   1 << 10,
	"kb":  1 << 10,
	"kib": 1 << 10,
	"m":   1 << 20,
	"mb":  1 << 20,
	"mib": 1 << 20,
	"g":   1 << 30,
	"gb":  1 << 30,
	"gib": 1 << 30,
	"t":   1 << 40,
	"tb":  1 << 40,
	"tib": 1 << 40,
}

// ParseByteSize reads a size written by a person in metadata and returns the
// byte count: "256m", "256 MB", "256MiB" and "268435456" all mean the same
// thing. A fractional part is allowed where it lands on a whole byte ("1.5g")
// and rejected where it does not ("1.5b").
//
// The value comes back as bytes because that is what the runtime takes. Podman
// reads `shm_size` in its create body as an int64 byte count and ignores a
// field it does not recognise rather than failing the create, so a size string
// that reached the wire would leave the container on the 64MB default with
// nothing said about it. Parsing here, at the field that was written, is what
// turns that silent no-op into a load error.
func ParseByteSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	// Caught before the split, because "-5m" has no leading digits and would
	// otherwise come back as "has no number" rather than the truth.
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("%q must be greater than zero", raw)
	}
	digits, suffix := splitSize(s)
	if digits == "" {
		return 0, fmt.Errorf("%q has no number", raw)
	}
	multiplier, known := sizeMultipliers[strings.ToLower(strings.TrimSpace(suffix))]
	if !known {
		return 0, fmt.Errorf("%q has an unknown suffix %q (use b, k, m, g, or no suffix for bytes)", raw, suffix)
	}
	// big.Rat keeps the multiply exact. A float64 would let a fraction times
	// 2^30 land on a byte count nobody wrote, and a metadata value that means
	// one thing at parse time and another at the runtime is worse than a
	// rejection.
	value, ok := new(big.Rat).SetString(digits)
	if !ok {
		return 0, fmt.Errorf("%q is not a number", raw)
	}
	value.Mul(value, new(big.Rat).SetInt64(multiplier))
	if !value.IsInt() {
		return 0, fmt.Errorf("%q is not a whole number of bytes", raw)
	}
	bytesBig := value.Num()
	if !bytesBig.IsInt64() {
		return 0, fmt.Errorf("%q is too large", raw)
	}
	bytes := bytesBig.Int64()
	if bytes <= 0 {
		return 0, fmt.Errorf("%q must be greater than zero", raw)
	}
	return bytes, nil
}

// splitSize splits the leading number from the trailing suffix. The scan takes
// digits and a decimal point only, so a sign or a letter stops it and the
// suffix lookup reports the whole value as unreadable instead of quietly
// dropping part of it.
func splitSize(s string) (digits, suffix string) {
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	return s[:i], s[i:]
}
