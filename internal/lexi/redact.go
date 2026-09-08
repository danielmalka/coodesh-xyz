package lexi

import (
	"crypto/sha256"
	"encoding/hex"
)

const senderHashBytes = 6

func hashPhone(from string) string {
	sum := sha256.Sum256([]byte(from))
	return hex.EncodeToString(sum[:senderHashBytes])
}
