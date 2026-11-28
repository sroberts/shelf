package kosync

import (
	"crypto/rand"
	"strings"
)

// passwordAlphabet leaves out letters and digits that look alike on an e-ink
// screen (l, 1, o, 0), since the password is typed on the reader's own
// on-screen keyboard, one letter at a time.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// GeneratePassword returns a password for a reader account: four groups of
// four lowercase letters and digits, such as "k7mq-2xrf-n4dp-9vht".
//
// Lowercase only and grouped, because it is typed on the reader, where every
// shift is a separate key press. Sixteen characters from 32 is 80 bits, which
// no amount of guessing over the internet gets near, so it is safe to put the
// reader's paths behind Funnel with it.
func GeneratePassword() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic("kosync: no randomness: " + err.Error()) // crypto/rand does not fail on supported platforms
	}
	var b strings.Builder
	for i, c := range buf {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(passwordAlphabet[int(c)%len(passwordAlphabet)])
	}
	return b.String()
}
