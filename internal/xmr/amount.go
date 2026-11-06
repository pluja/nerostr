package xmr

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Piconero is the smallest Monero unit: 1 XMR = 10^12 piconero.
type Piconero uint64

const (
	decimals = 12
	OneXMR   = Piconero(1_000_000_000_000)
)

// ParseXMR parses a decimal XMR amount such as "0.002" without going through floats.
func ParseXMR(s string) (Piconero, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > decimals {
		return 0, fmt.Errorf("amount %q has more than %d decimal places", s, decimals)
	}
	for _, part := range []string{whole, frac} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("invalid amount %q", s)
			}
		}
	}
	w, err := strconv.ParseUint(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", s, err)
	}
	if w > math.MaxUint64/uint64(OneXMR) {
		return 0, fmt.Errorf("amount %q is too large", s)
	}
	var f uint64
	if frac != "" {
		f, err = strconv.ParseUint(frac+strings.Repeat("0", decimals-len(frac)), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid amount %q: %w", s, err)
		}
	}
	total := w*uint64(OneXMR) + f
	if total < f {
		return 0, fmt.Errorf("amount %q is too large", s)
	}
	return Piconero(total), nil
}

// String formats the amount in XMR with trailing zeros trimmed, e.g. "0.002".
func (p Piconero) String() string {
	whole := uint64(p) / uint64(OneXMR)
	frac := uint64(p) % uint64(OneXMR)
	if frac == 0 {
		return strconv.FormatUint(whole, 10)
	}
	f := strings.TrimRight(fmt.Sprintf("%012d", frac), "0")
	return strconv.FormatUint(whole, 10) + "." + f
}

// Sub returns p-q, or 0 if q > p.
func (p Piconero) Sub(q Piconero) Piconero {
	if q > p {
		return 0
	}
	return p - q
}
