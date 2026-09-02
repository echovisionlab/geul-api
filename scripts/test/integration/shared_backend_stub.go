//go:build !integration

package main

import (
	"context"
	"fmt"
)

func startSuiteBackend(context.Context, string, string) (*suiteBackend, error) {
	return nil, fmt.Errorf("integration suite backend requires the integration build tag")
}
