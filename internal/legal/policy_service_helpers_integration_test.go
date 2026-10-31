//go:build integration

package legal_test

import (
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"gorm.io/gorm"
)

// legalPolicyTestOption completes the Deps of a test legal policy service.
type legalPolicyTestOption func(*legaldomain.Deps)

func withLegalContentBlocks(store *contentblock.Store) legalPolicyTestOption {
	return func(deps *legaldomain.Deps) { deps.ContentBlocks = store }
}

func newLegalPolicyTestService(
	db *gorm.DB,
	baseURL string,
	audit domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	dependencies legaldomain.Dependencies,
	options ...legalPolicyTestOption,
) *legaldomain.Service {
	deps := legaldomain.Deps{
		DB: db, SpiceDB: spiceDB, OG: dependencies.OG, Notice: dependencies.Notice, Audit: audit, BaseURL: baseURL,
		PreviewLinks: sharelinkadapter.LegalPreviewLinks{}, Members: memberadapter.PreferredLocales{},
	}
	for _, option := range options {
		option(&deps)
	}
	service, err := legaldomain.NewService(deps)
	if err != nil {
		panic(err)
	}
	return service
}

func newTestTermsService(
	db *gorm.DB,
	baseURL string,
	audit domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	dependencies legaldomain.Dependencies,
	options ...legalPolicyTestOption,
) *legaldomain.TermsConnectHandler {
	return legaldomain.NewTermsConnectHandler(newLegalPolicyTestService(db, baseURL, audit, spiceDB, dependencies, options...))
}

func newTestPrivacyService(
	db *gorm.DB,
	baseURL string,
	audit domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	dependencies legaldomain.Dependencies,
	options ...legalPolicyTestOption,
) *legaldomain.PrivacyConnectHandler {
	return legaldomain.NewPrivacyConnectHandler(newLegalPolicyTestService(db, baseURL, audit, spiceDB, dependencies, options...))
}

func newRecordedTestTermsService(
	db *gorm.DB,
	baseURL string,
	spiceDB *auth.SpiceDBClient,
	dependencies legaldomain.Dependencies,
	options ...legalPolicyTestOption,
) *legaldomain.TermsConnectHandler {
	return newTestTermsService(db, baseURL, &audittest.Recorder{}, spiceDB, dependencies, options...)
}

func newRecordedTestPrivacyService(
	db *gorm.DB,
	baseURL string,
	spiceDB *auth.SpiceDBClient,
	dependencies legaldomain.Dependencies,
	options ...legalPolicyTestOption,
) *legaldomain.PrivacyConnectHandler {
	return newTestPrivacyService(db, baseURL, &audittest.Recorder{}, spiceDB, dependencies, options...)
}

func newTestInternalLegalService(
	db *gorm.DB,
	audit domainaudit.Appender,
	dependencies legaldomain.Dependencies,
	store *contentblock.Store,
	permissions *auth.SpiceDBClient,
	checkpoints persistencecheckpoint.ContributorFence,
) *legaldomain.InternalService {
	service, err := legaldomain.NewInternalService(legaldomain.InternalDeps{
		DB: db, ContentBlocks: store, SpiceDB: permissions, Checkpoints: checkpoints,
		OG: dependencies.OG, Audit: audit,
	})
	if err != nil {
		panic(err)
	}
	return service
}

func newTestInternalTermsService(
	db *gorm.DB,
	audit domainaudit.Appender,
	dependencies legaldomain.Dependencies,
	store *contentblock.Store,
	permissions *auth.SpiceDBClient,
	checkpoints persistencecheckpoint.ContributorFence,
) *legaldomain.InternalTermsConnectHandler {
	return legaldomain.NewInternalTermsConnectHandler(newTestInternalLegalService(db, audit, dependencies, store, permissions, checkpoints))
}

func newTestInternalPrivacyService(
	db *gorm.DB,
	audit domainaudit.Appender,
	dependencies legaldomain.Dependencies,
	store *contentblock.Store,
	permissions *auth.SpiceDBClient,
	checkpoints persistencecheckpoint.ContributorFence,
) *legaldomain.InternalPrivacyConnectHandler {
	return legaldomain.NewInternalPrivacyConnectHandler(newTestInternalLegalService(db, audit, dependencies, store, permissions, checkpoints))
}
