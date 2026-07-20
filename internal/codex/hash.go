package codex

import (
	"crypto/sha256"
	"encoding/hex"
)

func hashRef(namespace, value string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + value))
	return hex.EncodeToString(sum[:12])
}
