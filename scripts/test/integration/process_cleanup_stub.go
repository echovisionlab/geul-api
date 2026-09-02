//go:build !integration

package main

func registerSuiteProcessCleanup(string, func() error) {}

func takeSuiteSignalCleanupOwnership() {}
