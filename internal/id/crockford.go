package id

// crockfordAlphabet is Crockford Base32: the digits plus uppercase letters with
// I, L, O, and U removed — killing 0/O and 1/I/L ambiguity and avoiding stray
// words. It is used ONLY for public_code generation and validation; nothing in
// this package encodes a Snowflake to Base32, because events have no public form.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// crockfordSymbol maps the low 5 bits of a byte to a Crockford symbol. The
// mapping is unbiased without rejection sampling precisely because 256 is an
// exact multiple of 32: each of the 32 symbols is produced by exactly 8 of the
// 256 byte values. Do NOT "fix" this into a biased `b % len(crockfordAlphabet)`.
func crockfordSymbol(b byte) byte { return crockfordAlphabet[b&0x1f] }

// normalizeCrockford folds one input rune toward its canonical Crockford symbol:
// lowercase letters are upper-cased, and the lenient substitutions O->0 and
// I/L->1 absorb the most common transcription mistakes. A rune still outside the
// alphabet after folding is returned unchanged for the caller's charset check to
// reject.
func normalizeCrockford(r rune) rune {
	switch r {
	case 'o', 'O':
		return '0'
	case 'i', 'I', 'l', 'L':
		return '1'
	default:
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
}
