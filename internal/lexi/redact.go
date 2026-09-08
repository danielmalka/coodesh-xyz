package lexi

import (
	"crypto/sha256"
	"encoding/hex"
)

func hashFrom(from string) string {
	sum := sha256.Sum256([]byte(from))
	return hex.EncodeToString(sum[:6])
}
