package cldr

import (
	"maps"
	"strings"

	"github.com/dmitrymomot/go-router/i18n"
)

// Format specs, keyed by locale tag. Specs are per-tag, never per-language:
// es-MX and es-ES pluralize identically and format nothing alike, which is why
// there is no "Format Spanish" — every var below carries a full region.
//
// The values are generated from CLDR 48 (unicode-org/cldr-json): separators,
// minimum grouping digits and the currency and percent patterns from
// cldr-numbers-full, and the short date, time and date-time patterns from
// cldr-dates-full. Invisible characters are written as escapes, so U+00A0
// NO-BREAK SPACE and U+202F NARROW NO-BREAK SPACE stay apart in a diff.
// testdata/formats.json holds strings rendered straight from the CLDR patterns,
// and a test renders every spec through package i18n against it.
//
// FormatSpec cannot express all of CLDR, so a few values are the closest it
// has:
//
//   - The year is always four digits. CLDR's short date writes two ("yy"),
//     which reads badly once a date leaves the current century.
//   - Go's time layout has no unpadded 24-hour hour, so a locale whose CLDR
//     time is "H:mm" renders "09:05" where CLDR writes "9:05".
//   - Go renders the AM/PM marker in English, and CLDR's day periods (B, as
//     zh-TW uses) render as AM/PM too.
//   - The currency and percent space is U+0020, where CLDR writes U+00A0.
//   - Digits are always ASCII, so locales whose CLDR digits are not (ar-SA,
//     ar-EG, fa-IR, bn-BD, mr-IN, ne-NP, my-MM) have no spec, and nor do the
//     locales of Indian grouping (hi-IN, en-IN: 12,34,567).
var (
	// FormatArAE is the CLDR format spec for ar-AE. The UAE uses Western digits
	// (latn); the bidi marks of the CLDR patterns are dropped.
	FormatArAE = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2/1/2006", TimeLayout: "3:04 PM", DateTimeLayout: "2/1/2006، 3:04 PM",
		CurrencySpace: true,
	}
	// FormatBgBG is the CLDR format spec for bg-BG. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatBgBG = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2.01.2006\u202fг.", TimeLayout: "15:04", DateTimeLayout: "2.01.2006\u202fг., 15:04",
		CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatCaES is the CLDR format spec for ca-ES. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatCaES = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/1/2006", TimeLayout: "15:04", DateTimeLayout: "2/1/2006 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatCsCZ is the CLDR format spec for cs-CZ. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatCsCZ = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatDaDK is the CLDR format spec for da-DK.
	FormatDaDK = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02.01.2006", TimeLayout: "15.04", DateTimeLayout: "02.01.2006 15.04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatDeAT is the CLDR format spec for de-AT.
	FormatDeAT = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencyBefore: true, CurrencySpace: true, PercentSpace: true,
	}
	// FormatDeCH is the CLDR format spec for de-CH.
	FormatDeCH = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: "'",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencyBefore: true, CurrencySpace: true,
	}
	// FormatDeDE is the CLDR format spec for de-DE.
	FormatDeDE = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatDeLU is the CLDR format spec for de-LU.
	FormatDeLU = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatElCY is the CLDR format spec for el-CY.
	FormatElCY = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/1/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "2/1/2006, 3:04\u202fPM",
		CurrencySpace: true,
	}
	// FormatElGR is the CLDR format spec for el-GR.
	FormatElGR = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/1/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "2/1/2006, 3:04\u202fPM",
		CurrencySpace: true,
	}
	// FormatEnAU is the CLDR format spec for en-AU.
	FormatEnAU = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2/1/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "2/1/2006, 3:04\u202fPM",
		CurrencyBefore: true,
	}
	// FormatEnCA is the CLDR format spec for en-CA.
	FormatEnCA = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2006-01-02", TimeLayout: "3:04\u202fPM", DateTimeLayout: "2006-01-02, 3:04\u202fPM",
		CurrencyBefore: true,
	}
	// FormatEnGB is the CLDR format spec for en-GB.
	FormatEnGB = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006, 15:04",
		CurrencyBefore: true,
	}
	// FormatEnIE is the CLDR format spec for en-IE.
	FormatEnIE = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006, 15:04",
		CurrencyBefore: true,
	}
	// FormatEnUS is the CLDR format spec for en-US.
	FormatEnUS = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "1/2/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "1/2/2006, 3:04\u202fPM",
		CurrencyBefore: true,
	}
	// FormatEsAR is the CLDR format spec for es-AR.
	FormatEsAR = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/1/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "2/1/2006, 3:04\u202fPM",
		CurrencyBefore: true, CurrencySpace: true,
	}
	// FormatEsCL is the CLDR format spec for es-CL.
	FormatEsCL = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02-01-2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "02-01-2006, 3:04\u202fPM",
		CurrencyBefore: true,
	}
	// FormatEsCO is the CLDR format spec for es-CO.
	FormatEsCO = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/01/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "2/01/2006, 3:04\u202fPM",
		CurrencyBefore: true, CurrencySpace: true,
	}
	// FormatEsES is the CLDR format spec for es-ES. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatEsES = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/1/2006", TimeLayout: "15:04", DateTimeLayout: "2/1/2006, 15:04",
		CurrencySpace: true, PercentSpace: true, MinGroupingDigits: 2,
	}
	// FormatEsMX is the CLDR format spec for es-MX.
	FormatEsMX = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "02/01/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "02/01/2006, 3:04\u202fPM",
		CurrencyBefore: true,
	}
	// FormatEtEE is the CLDR format spec for et-EE.
	FormatEtEE = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006 15:04",
		CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatEuES is the CLDR format spec for eu-ES.
	FormatEuES = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2006/1/2", TimeLayout: "15:04", DateTimeLayout: "2006/1/2 (15:04)",
		CurrencySpace: true, PercentSpace: true, PercentBefore: true,
	}
	// FormatFiFI is the CLDR format spec for fi-FI. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatFiFI = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2.1.2006", TimeLayout: "15.04", DateTimeLayout: "2.1.2006 15.04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatFilPH is the CLDR format spec for fil-PH.
	FormatFilPH = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "1/2/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "1/2/2006, 3:04\u202fPM",
		CurrencyBefore: true,
	}
	// FormatFrBE is the CLDR format spec for fr-BE.
	FormatFrBE = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u202f",
		DateLayout: "2/01/2006", TimeLayout: "15:04", DateTimeLayout: "2/01/2006 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatFrCA is the CLDR format spec for fr-CA.
	FormatFrCA = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2006-01-02", TimeLayout: "15 h 04", DateTimeLayout: "2006-01-02 15 h 04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatFrCH is the CLDR format spec for fr-CH.
	FormatFrCH = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "'",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006 15:04",
		CurrencySpace: true,
	}
	// FormatFrFR is the CLDR format spec for fr-FR.
	FormatFrFR = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u202f",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatFrLU is the CLDR format spec for fr-LU.
	FormatFrLU = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatGaIE is the CLDR format spec for ga-IE.
	FormatGaIE = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2/1/2006", TimeLayout: "15:04", DateTimeLayout: "2/1/2006 15:04",
		CurrencyBefore: true,
	}
	// FormatGlES is the CLDR format spec for gl-ES.
	FormatGlES = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006, 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatHeIL is the CLDR format spec for he-IL. The bidi marks of the CLDR
	// patterns are dropped. CLDR's hour is unpadded (H); Go pads it.
	FormatHeIL = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2.1.2006", TimeLayout: "15:04", DateTimeLayout: "2.1.2006, 15:04",
		CurrencySpace: true,
	}
	// FormatHrHR is the CLDR format spec for hr-HR.
	FormatHrHR = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02. 01. 2006.", TimeLayout: "15:04", DateTimeLayout: "02. 01. 2006. 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatHuHU is the CLDR format spec for hu-HU. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatHuHU = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2006. 01. 02.", TimeLayout: "15:04", DateTimeLayout: "2006. 01. 02. 15:04",
		CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatIdID is the CLDR format spec for id-ID.
	FormatIdID = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02/01/2006", TimeLayout: "15.04", DateTimeLayout: "02/01/2006 15.04",
		CurrencyBefore: true,
	}
	// FormatItCH is the CLDR format spec for it-CH.
	FormatItCH = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: "'",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencyBefore: true, CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatItIT is the CLDR format spec for it-IT.
	FormatItIT = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006, 15:04",
		CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatJaJP is the CLDR format spec for ja-JP. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatJaJP = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2006/01/02", TimeLayout: "15:04", DateTimeLayout: "2006/01/02 15:04",
		CurrencyBefore: true,
	}
	// FormatKoKR is the CLDR format spec for ko-KR. The AM/PM marker leads the
	// time, as CLDR puts it.
	FormatKoKR = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2006. 1. 2.", TimeLayout: "PM 3:04", DateTimeLayout: "2006. 1. 2. PM 3:04",
		CurrencyBefore: true,
	}
	// FormatLtLT is the CLDR format spec for lt-LT.
	FormatLtLT = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2006-01-02", TimeLayout: "15:04", DateTimeLayout: "2006-01-02 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatLvLV is the CLDR format spec for lv-LV.
	FormatLvLV = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006 15:04",
		CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatMsMY is the CLDR format spec for ms-MY.
	FormatMsMY = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2/01/2006", TimeLayout: "3:04\u202fPM", DateTimeLayout: "2/01/2006, 3:04\u202fPM",
		CurrencyBefore: true,
	}
	// FormatMtMT is the CLDR format spec for mt-MT.
	FormatMtMT = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006 15:04",
		CurrencyBefore: true,
	}
	// FormatNbNO is the CLDR format spec for nb-NO.
	FormatNbNO = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatNlBE is the CLDR format spec for nl-BE.
	FormatNlBE = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/01/2006", TimeLayout: "15:04", DateTimeLayout: "2/01/2006, 15:04",
		CurrencyBefore: true, CurrencySpace: true,
	}
	// FormatNlNL is the CLDR format spec for nl-NL.
	FormatNlNL = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02-01-2006", TimeLayout: "15:04", DateTimeLayout: "02-01-2006, 15:04",
		CurrencyBefore: true, CurrencySpace: true,
	}
	// FormatPlPL is the CLDR format spec for pl-PL.
	FormatPlPL = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2.01.2006", TimeLayout: "15:04", DateTimeLayout: "2.01.2006, 15:04",
		CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatPtBR is the CLDR format spec for pt-BR. CLDR's pt root is Brazilian
	// Portuguese.
	FormatPtBR = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006 15:04",
		CurrencyBefore: true, CurrencySpace: true,
	}
	// FormatPtPT is the CLDR format spec for pt-PT.
	FormatPtPT = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02/01/2006", TimeLayout: "15:04", DateTimeLayout: "02/01/2006, 15:04",
		CurrencySpace: true, MinGroupingDigits: 2,
	}
	// FormatRoRO is the CLDR format spec for ro-RO.
	FormatRoRO = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatRuRU is the CLDR format spec for ru-RU.
	FormatRuRU = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatSkSK is the CLDR format spec for sk-SK. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatSkSK = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2. 1. 2006", TimeLayout: "15:04", DateTimeLayout: "2. 1. 2006 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatSlSI is the CLDR format spec for sl-SI.
	FormatSlSI = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2. 1. 2006", TimeLayout: "15:04", DateTimeLayout: "2. 1. 2006, 15:04",
		CurrencySpace: true, PercentSpace: true, MinGroupingDigits: 2,
	}
	// FormatSrRS is the CLDR format spec for sr-RS.
	FormatSrRS = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2. 1. 2006.", TimeLayout: "15:04", DateTimeLayout: "2. 1. 2006. 15:04",
		CurrencySpace: true,
	}
	// FormatSvFI is the CLDR format spec for sv-FI. CLDR's hour is unpadded (H);
	// Go pads it.
	FormatSvFI = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2.1.2006", TimeLayout: "15.04", DateTimeLayout: "2.1.2006 15.04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatSvSE is the CLDR format spec for sv-SE.
	FormatSvSE = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "2006-01-02", TimeLayout: "15:04", DateTimeLayout: "2006-01-02 15:04",
		CurrencySpace: true, PercentSpace: true,
	}
	// FormatThTH is the CLDR format spec for th-TH.
	FormatThTH = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2/1/2006", TimeLayout: "15:04", DateTimeLayout: "2/1/2006 15:04",
		CurrencyBefore: true,
	}
	// FormatTrTR is the CLDR format spec for tr-TR.
	FormatTrTR = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2.01.2006", TimeLayout: "15:04", DateTimeLayout: "2.01.2006 15:04",
		CurrencyBefore: true, PercentBefore: true,
	}
	// FormatUkUA is the CLDR format spec for uk-UA.
	FormatUkUA = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: "\u00a0",
		DateLayout: "02.01.2006", TimeLayout: "15:04", DateTimeLayout: "02.01.2006, 15:04",
		CurrencySpace: true,
	}
	// FormatUrPK is the CLDR format spec for ur-PK.
	FormatUrPK = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2/1/2006", TimeLayout: "3:04 PM", DateTimeLayout: "2/1/2006 3:04 PM",
		CurrencyBefore: true,
	}
	// FormatViVN is the CLDR format spec for vi-VN.
	FormatViVN = i18n.FormatSpec{
		DecimalSep: ",", GroupSep: ".",
		DateLayout: "2/1/2006", TimeLayout: "15:04", DateTimeLayout: "15:04 2/1/2006",
		CurrencySpace: true,
	}
	// FormatZhCN is the CLDR format spec for zh-CN. CLDR's zh root is Mainland
	// Chinese.
	FormatZhCN = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2006/1/2", TimeLayout: "15:04", DateTimeLayout: "2006/1/2 15:04",
		CurrencyBefore: true,
	}
	// FormatZhHK is the CLDR format spec for zh-HK. From CLDR's zh-Hant-HK.
	FormatZhHK = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2/1/2006", TimeLayout: "PM3:04", DateTimeLayout: "2/1/2006\u2009PM3:04",
		CurrencyBefore: true,
	}
	// FormatZhTW is the CLDR format spec for zh-TW. From CLDR's zh-Hant. CLDR's
	// day period (B) renders as AM/PM.
	FormatZhTW = i18n.FormatSpec{
		DecimalSep: ".", GroupSep: ",",
		DateLayout: "2006/1/2", TimeLayout: "PM3:04", DateTimeLayout: "2006/1/2\u2009PM3:04",
		CurrencyBefore: true,
	}
)

// allFormats maps locale tag to spec. Keep it in sync with the vars above;
// TestFormatsMatchCLDRGolden pins the full set.
var allFormats = map[string]i18n.FormatSpec{
	"ar-AE":  FormatArAE,
	"bg-BG":  FormatBgBG,
	"ca-ES":  FormatCaES,
	"cs-CZ":  FormatCsCZ,
	"da-DK":  FormatDaDK,
	"de-AT":  FormatDeAT,
	"de-CH":  FormatDeCH,
	"de-DE":  FormatDeDE,
	"de-LU":  FormatDeLU,
	"el-CY":  FormatElCY,
	"el-GR":  FormatElGR,
	"en-AU":  FormatEnAU,
	"en-CA":  FormatEnCA,
	"en-GB":  FormatEnGB,
	"en-IE":  FormatEnIE,
	"en-US":  FormatEnUS,
	"es-AR":  FormatEsAR,
	"es-CL":  FormatEsCL,
	"es-CO":  FormatEsCO,
	"es-ES":  FormatEsES,
	"es-MX":  FormatEsMX,
	"et-EE":  FormatEtEE,
	"eu-ES":  FormatEuES,
	"fi-FI":  FormatFiFI,
	"fil-PH": FormatFilPH,
	"fr-BE":  FormatFrBE,
	"fr-CA":  FormatFrCA,
	"fr-CH":  FormatFrCH,
	"fr-FR":  FormatFrFR,
	"fr-LU":  FormatFrLU,
	"ga-IE":  FormatGaIE,
	"gl-ES":  FormatGlES,
	"he-IL":  FormatHeIL,
	"hr-HR":  FormatHrHR,
	"hu-HU":  FormatHuHU,
	"id-ID":  FormatIdID,
	"it-CH":  FormatItCH,
	"it-IT":  FormatItIT,
	"ja-JP":  FormatJaJP,
	"ko-KR":  FormatKoKR,
	"lt-LT":  FormatLtLT,
	"lv-LV":  FormatLvLV,
	"ms-MY":  FormatMsMY,
	"mt-MT":  FormatMtMT,
	"nb-NO":  FormatNbNO,
	"nl-BE":  FormatNlBE,
	"nl-NL":  FormatNlNL,
	"pl-PL":  FormatPlPL,
	"pt-BR":  FormatPtBR,
	"pt-PT":  FormatPtPT,
	"ro-RO":  FormatRoRO,
	"ru-RU":  FormatRuRU,
	"sk-SK":  FormatSkSK,
	"sl-SI":  FormatSlSI,
	"sr-RS":  FormatSrRS,
	"sv-FI":  FormatSvFI,
	"sv-SE":  FormatSvSE,
	"th-TH":  FormatThTH,
	"tr-TR":  FormatTrTR,
	"uk-UA":  FormatUkUA,
	"ur-PK":  FormatUrPK,
	"vi-VN":  FormatViVN,
	"zh-CN":  FormatZhCN,
	"zh-HK":  FormatZhHK,
	"zh-TW":  FormatZhTW,
}

// AllFormats returns every spec keyed by locale tag. The returned map is a
// copy; mutating it affects nothing.
func AllFormats() map[string]i18n.FormatSpec {
	out := make(map[string]i18n.FormatSpec, len(allFormats))
	maps.Copy(out, allFormats)
	return out
}

// FormatFor looks up a spec by locale tag. ok is false for a tag this package
// does not carry — the caller then leaves it unwired and package i18n's
// Invariant applies. Lookup is by exact tag: there is no base-language
// fallback, because a language does not have a number format (there is no
// "Format Spanish").
//
// This is a convenience for callers. package i18n never calls it.
func FormatFor(tag string) (i18n.FormatSpec, bool) {
	t := strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")
	lang, region, ok := strings.Cut(t, "-")
	if !ok {
		return i18n.FormatSpec{}, false
	}
	s, found := allFormats[strings.ToLower(lang)+"-"+strings.ToUpper(region)]
	return s, found
}
