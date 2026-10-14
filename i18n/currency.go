package i18n

// maxCurrencyDigits bounds the divisor computation in appendMinorMag to
// uint64's safe decimal range (10^19 < 2^64 <= 10^20). No real ISO-4217
// currency has more than 4 minor units (CLF); this is a defensive backstop
// against a pathological Money.Digits, not a realistic path. Without it, a
// Digits of 20 or more would overflow the repeated-multiply divisor,
// corrupting the rendered digits, and a Digits of 64 or more would wrap
// it to exactly zero, turning the following division into a panic.
const maxCurrencyDigits = 19

// Money is an amount for [Bundle.Currency]: Minor counts the minor units of
// the currency, Digits says how many minor digits make one major unit (2 for
// USD, 0 for JPY, 3 for BHD), and Symbol is what to print beside the number.
// This package knows no currencies, so the application fills all three from
// its own money type:
//
//	i18n.Money{Minor: 123456, Digits: 2, Symbol: "$"} // $1,234.56 under en-US
type Money struct {
	Symbol string
	Minor  int64
	Digits int
}

// Currency formats m for the locale. The amount and symbol come from m, and
// the locale contributes only separators and symbol placement. A de-DE reader
// viewing USD therefore sees "1.234,56 $", which is correct: multi-currency
// rendering falls out of the split for free.
func (b *Bundle) Currency(loc Locale, m Money) string {
	return string(b.AppendCurrency(make([]byte, 0, 32), loc, m))
}

// AppendCurrency is Currency appending into dst.
func (b *Bundle) AppendCurrency(dst []byte, loc Locale, m Money) []byte {
	return appendCurrencySpec(dst, m, b.specFor(b.locIdx(loc)))
}

// appendCurrencySpec renders m under s. Shared by Bundle and Localizer,
// which must not re-implement this.
func appendCurrencySpec(dst []byte, m Money, s *FormatSpec) []byte {
	// The sign always leads, whichever side the symbol sits on: "-$1.00" and
	// "-1,00 €". Everything past this point sees only the magnitude.
	if m.Minor < 0 {
		dst = append(dst, '-')
	}
	mag := absU64(m.Minor) // shared with NumberInt; see format.go

	if s.CurrencyBefore {
		dst = append(dst, m.Symbol...)
		if s.CurrencySpace {
			dst = append(dst, ' ')
		}
		return appendMinorMag(dst, mag, m.Digits, s)
	}
	dst = appendMinorMag(dst, mag, m.Digits, s)
	if s.CurrencySpace {
		dst = append(dst, ' ')
	}
	return append(dst, m.Symbol...)
}

// appendMinorMag renders a minor-unit magnitude as major.minor under s. The
// caller has already emitted any sign. units <= 0 (JPY's 0, or a malformed
// negative value) renders no fractional part at all.
func appendMinorMag(dst []byte, mag uint64, units int, s *FormatSpec) []byte {
	if units <= 0 {
		return appendGroupedUint(dst, mag, s.GroupSep)
	}
	if units > maxCurrencyDigits {
		units = maxCurrencyDigits
	}
	div := uint64(1)
	for range units {
		div *= 10
	}
	dst = appendGroupedUint(dst, mag/div, s.GroupSep)
	dst = append(dst, s.DecimalSep...)
	// Emit exactly `units` fractional digits, most significant first. frac <
	// div always holds, so this both pads and truncates correctly: 5 minor
	// USD is "05", 0 is "00", 5 minor BHD is "005". No branches, no
	// allocation.
	frac := mag % div
	for p := div / 10; p > 0; p /= 10 {
		dst = append(dst, byte('0'+(frac/p)%10))
	}
	return dst
}
