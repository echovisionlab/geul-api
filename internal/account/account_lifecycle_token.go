package account

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

const (
	DeletionGracePeriod = 30 * 24 * time.Hour
	DeletionTokenExpiry = 24 * time.Hour
)

func generateToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
