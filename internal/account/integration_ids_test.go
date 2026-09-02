//go:build integration

package account

import "github.com/google/uuid"

func integrationTestUUID() string {
	return uuid.NewString()
}
