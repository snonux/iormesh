package k8s

import (
	"fmt"
	"math/big"
	"regexp"
)

// A trace emptyDir's sizeLimit is a Kubernetes resource.Quantity, which
// the API server keeps in the form it was given: binary SI (512Mi, 2Gi),
// decimal SI (2G, 2000M), a plain byte count (2147483648) or a decimal
// exponent (2e9, 2E9), with or without a fraction (1.5Gi). TraceCap
// reads them all (task 5g4): a debug deploy takes the sizeLimit from
// the pod as its owner wrote it, and refusing a valid "2G" left such a
// pod untraceable for no reason.

// quantityRE is the subset of resource.Quantity a byte size uses: no
// sign (a size is never negative), no milli/micro/nano suffix (a
// fraction of a byte is no size), and an exponent of at most two digits
// (an "1e999999999" must not make math/big build a huge number). The
// length cap keeps the parse trivial.
var quantityRE = regexp.MustCompile(
	`^([0-9]+(\.[0-9]*)?|\.[0-9]+)(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E|[eE][+-]?[0-9]{1,2})?$`)

// maxQuantityLen bounds the sizeLimit text parseQuantity looks at.
const maxQuantityLen = 40

// maxQuantity is the largest size parseQuantity returns: 2^62 bytes
// (4 EiB), far past any volume, and with room to compute in uint64.
const maxQuantity = 1 << 62

// quantitySuffix is each suffix's multiplier.
var quantitySuffix = map[string]*big.Rat{
	"":   big.NewRat(1, 1),
	"Ki": new(big.Rat).SetInt64(1 << 10), "Mi": new(big.Rat).SetInt64(1 << 20),
	"Gi": new(big.Rat).SetInt64(1 << 30), "Ti": new(big.Rat).SetInt64(1 << 40),
	"Pi": new(big.Rat).SetInt64(1 << 50), "Ei": new(big.Rat).SetInt64(1 << 60),
	"k": new(big.Rat).SetInt64(1e3), "M": new(big.Rat).SetInt64(1e6),
	"G": new(big.Rat).SetInt64(1e9), "T": new(big.Rat).SetInt64(1e12),
	"P": new(big.Rat).SetInt64(1e15), "E": new(big.Rat).SetInt64(1e18),
}

// parseQuantity returns the bytes a sizeLimit stands for, rounded down
// to a whole byte, or an error for text that is no byte size, or one
// over maxQuantity.
func parseQuantity(s string) (uint64, error) {
	m := quantityRE.FindStringSubmatch(s)
	if len(s) > maxQuantityLen || m == nil {
		return 0, fmt.Errorf("%q is not a size (want e.g. 512Mi, 2Gi, 2G or a byte count)", s)
	}
	num, suffix := m[1], m[3]
	n, ok := new(big.Rat).SetString(num)
	if !ok {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	if mult, isSI := quantitySuffix[suffix]; isSI {
		n.Mul(n, mult)
	} else {
		// An exponent: the regexp left one of e/E, a sign and 1–2 digits,
		// which big.Rat reads as the exponent of 1.
		exp, ok := new(big.Rat).SetString("1" + suffix)
		if !ok {
			return 0, fmt.Errorf("%q is not a size", s)
		}
		n.Mul(n, exp)
	}
	whole := new(big.Int).Quo(n.Num(), n.Denom())
	if whole.Cmp(big.NewInt(maxQuantity)) > 0 {
		return 0, fmt.Errorf("%q is over %d bytes", s, uint64(maxQuantity))
	}
	return whole.Uint64(), nil
}
