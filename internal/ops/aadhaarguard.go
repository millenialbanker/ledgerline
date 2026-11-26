package ops

import (
	"regexp"
)

// ---- no full Aadhaar numbers in uploaded documents ----
//
// The Aadhaar Act s.29 and UIDAI's Aadhaar Data Vault circular: a full
// Aadhaar number is kept only in an encrypted Aadhaar Data Vault, and
// copies are masked (only the last four digits shown). Ledgerline keeps
// only the last four digits of the numbers it takes (digilocker.go,
// kyc/offline.go, nominees.go). On the branch path staff upload copies of
// proofs; an Aadhaar card copy must be the masked one. Ledgerline cannot
// read the digits in a photograph, so:
//
//   - a document whose text carries a full Aadhaar number (12 digits in the
//     printed 4-4-4 form, a valid Verhoeff check digit, not starting with
//     0 or 1) is refused, with how to get a masked copy;
//   - the document kind "Aadhaar (masked)" lets staff say what they are
//     uploading, and the help tells them to use the masked Aadhaar.
//
// A scanned image of a full Aadhaar card still gets through: masking it
// is the uploader's step, which the upload screen asks for.

// DocAadhaarMasked is the kind for a masked Aadhaar copy.
const DocAadhaarMasked = "Aadhaar (masked)"

var reAadhaarPrinted = regexp.MustCompile(`(^|[^0-9])([2-9][0-9]{3})[ \-]([0-9]{4})[ \-]([0-9]{4})([^0-9]|$)`)

// verhoeff tables.
var (
	vhD = [10][10]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 2, 3, 4, 0, 6, 7, 8, 9, 5}, {2, 3, 4, 0, 1, 7, 8, 9, 5, 6}, {3, 4, 0, 1, 2, 8, 9, 5, 6, 7}, {4, 0, 1, 2, 3, 9, 5, 6, 7, 8},
		{5, 9, 8, 7, 6, 0, 4, 3, 2, 1}, {6, 5, 9, 8, 7, 1, 0, 4, 3, 2}, {7, 6, 5, 9, 8, 2, 1, 0, 4, 3}, {8, 7, 6, 5, 9, 3, 2, 1, 0, 4}, {9, 8, 7, 6, 5, 4, 3, 2, 1, 0}}
	vhP = [8][10]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 5, 7, 6, 2, 8, 3, 0, 9, 4}, {5, 8, 0, 3, 7, 9, 6, 1, 4, 2}, {8, 9, 1, 6, 0, 4, 3, 5, 2, 7},
		{9, 4, 5, 3, 1, 2, 6, 8, 7, 0}, {4, 2, 8, 6, 5, 7, 3, 9, 0, 1}, {2, 7, 9, 3, 8, 0, 6, 4, 1, 5}, {7, 0, 4, 6, 9, 1, 3, 2, 5, 8}}
)

// verhoeffValid checks a number's last digit as its Verhoeff check digit.
func verhoeffValid(num string) bool {
	c := 0
	for i := 0; i < len(num); i++ {
		d := num[len(num)-1-i]
		if d < '0' || d > '9' {
			return false
		}
		c = vhD[c][vhP[i%8][d-'0']]
	}
	return c == 0
}

// fullAadhaarIn reports whether data's text carries a full Aadhaar number.
func fullAadhaarIn(data []byte) bool {
	for _, m := range reAadhaarPrinted.FindAllSubmatch(data, 50) {
		if verhoeffValid(string(m[2]) + string(m[3]) + string(m[4])) {
			return true
		}
	}
	return false
}

// aadhaarGuard refuses a document carrying a full Aadhaar number.
func aadhaarGuard(data []byte) error {
	if fullAadhaarIn(data) {
		return invalid("this document shows a full Aadhaar number. Upload the masked Aadhaar instead (only the last four digits shown: download it from the UIDAI site with \"Masked Aadhaar\", or cover the first eight digits before scanning)")
	}
	return nil
}
