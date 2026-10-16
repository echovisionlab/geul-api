package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// suiteBackendResetter is the band runner's only backend dependency. Lease
// serialization and resource ownership stay with the suite composition root.
type suiteBackendResetter interface {
	Reset(context.Context) error
}

func runSerialIntegrationBand(
	ctx context.Context,
	packages []string,
	backend suiteBackendResetter,
	runPackage func(string) error,
) error {
	packageErrors := make([]error, 0, len(packages))
	for _, packagePath := range packages {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(packageErrors, err)...)
		}
		if err := resetSuiteBackend(ctx, backend); err != nil {
			packageErrors = append(
				packageErrors,
				fmt.Errorf("reset suite backend before %s: %w", packagePath, err),
			)
			return errors.Join(packageErrors...)
		}

		if err := ctx.Err(); err != nil {
			return errors.Join(append(packageErrors, err)...)
		}
		commandErr := runPackage(packagePath)
		resetErr := resetSuiteBackend(context.WithoutCancel(ctx), backend)
		if commandErr != nil {
			packageErrors = append(
				packageErrors,
				fmt.Errorf("run integration package %s: %w", packagePath, commandErr),
			)
		}
		if resetErr != nil {
			packageErrors = append(
				packageErrors,
				fmt.Errorf("reset suite backend after %s: %w", packagePath, resetErr),
			)
			return errors.Join(packageErrors...)
		}
	}
	return errors.Join(packageErrors...)
}

func resetSuiteBackend(parent context.Context, backend suiteBackendResetter) error {
	if backend == nil {
		return nil
	}
	resetCtx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	return backend.Reset(resetCtx)
}
