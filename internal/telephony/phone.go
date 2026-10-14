package telephony

import (
	"strings"
)

// NormalizeNumber tidies a phone number as typed: spaces (incl. no-break
// ones), dots, dashes, slashes and brackets go, and a leading "00"
// becomes "+". Nothing else changes, so Valid can still reject it.
func NormalizeNumber(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch r {
		case ' ', ' ', ' ', '\t', '.', '-', '(', ')', '/', '‐', '–':
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if strings.HasPrefix(out, "00") {
		out = "+" + out[2:]
	}
	return out
}

// NumberProblem says what is wrong with a normalised number.
type NumberProblem int

const (
	NumberOK       NumberProblem = iota
	NumberEmpty                  // nothing typed
	NumberNoPlus                 // does not start with + and a country code
	NumberBadShape               // not 8–15 digits after the +, or other characters
)

// CheckNumber validates an E.164 number (after NormalizeNumber): "+",
// a first digit 1–9, then 7–14 more digits.
func CheckNumber(s string) NumberProblem {
	switch {
	case s == "":
		return NumberEmpty
	case s[0] != '+':
		return NumberNoPlus
	}
	d := s[1:]
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return NumberBadShape
	}
	for _, r := range d {
		if r < '0' || r > '9' {
			return NumberBadShape
		}
	}
	return NumberOK
}

// ValidNumber reports whether s is an E.164 number.
func ValidNumber(s string) bool { return CheckNumber(s) == NumberOK }

// callingCodes maps country calling codes to an ISO 3166 region, for the
// "Calling …" hint. +1 and +7 are shared by several countries and left
// unnamed.
var callingCodes = map[string]string{
	"20": "EG", "27": "ZA", "30": "GR", "31": "NL", "32": "BE", "33": "FR", "34": "ES", "36": "HU",
	"39": "IT", "40": "RO", "41": "CH", "43": "AT", "44": "GB", "45": "DK", "46": "SE", "47": "NO",
	"48": "PL", "49": "DE", "51": "PE", "52": "MX", "53": "CU", "54": "AR", "55": "BR", "56": "CL",
	"57": "CO", "58": "VE", "60": "MY", "61": "AU", "62": "ID", "63": "PH", "64": "NZ", "65": "SG",
	"66": "TH", "81": "JP", "82": "KR", "84": "VN", "86": "CN", "90": "TR", "91": "IN", "92": "PK",
	"234": "NG", "254": "KE", "351": "PT", "352": "LU", "353": "IE", "354": "IS", "358": "FI",
	"380": "UA", "420": "CZ", "852": "HK", "853": "MO", "886": "TW", "966": "SA", "971": "AE", "972": "IL",
}

// Region returns the ISO 3166 region of a number's calling code, or ""
// when it is unknown or shared (+1, +7).
func Region(number string) string {
	if !strings.HasPrefix(number, "+") {
		return ""
	}
	d := number[1:]
	for n := 3; n >= 1; n-- {
		if len(d) > n {
			if r, ok := callingCodes[d[:n]]; ok {
				return r
			}
		}
	}
	return ""
}

// MaskNumber hides the middle of a number for lists: "+1••••••0123".
func MaskNumber(s string) string {
	const tail = 4
	head := 0
	if strings.HasPrefix(s, "+") {
		head = 1 + len(callingCodeOf(s))
	}
	if len(s)-head-tail < 2 {
		return s
	}
	return s[:head] + strings.Repeat("•", len(s)-head-tail) + s[len(s)-tail:]
}

func callingCodeOf(s string) string {
	d := strings.TrimPrefix(s, "+")
	for n := 3; n >= 1; n-- {
		if len(d) > n {
			if _, ok := callingCodes[d[:n]]; ok {
				return d[:n]
			}
		}
	}
	if len(d) > 0 {
		return d[:1]
	}
	return ""
}
