package trajectory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func SHA256Hex(value any) string {
	switch v := value.(type) {
	case string:
		sum := sha256.Sum256([]byte(v))
		return hex.EncodeToString(sum[:])
	default:
		raw, _ := json.Marshal(v)
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
}
