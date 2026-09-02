//go:build integration

package application

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var translationIntegrationSuite *testutil.OryIntegrationSuite

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Translation Ory integration suite: %v\n", err)
		os.Exit(1)
	}
	translationIntegrationSuite = suite
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup Translation integration runtime: %v\n", err)
		code = 1
	}
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup Translation Ory integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func seedRunningMenuTranslationJob(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
) (*model.TranslationJob, *translation.Candidate) {
	t.Helper()
	menuID := generateUUID()
	documentID := generateUUID()
	revision := generateUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, 'compact', ?, ?, ?)`,
		documentID,
		revision,
		now,
		now,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO menu (id, name, items, source_locale, content_document_id, created_at, updated_at)
		 VALUES (?, ?, CAST(? AS jsonb), 'en', ?, ?, ?)`,
		menuID,
		"translation-provider-document-"+menuID,
		`[{"id":"source-item","label":"Source menu","linkType":"custom","url":"/source"}]`,
		documentID,
		now,
		now,
	).Error)
	xliff := []byte("request")
	manifest := []byte("{}")
	requestedByMemberID := testutil.IntegrationUUID()
	testutil.InsertDocumentContributor(t, db, requestedByMemberID)
	job := &model.TranslationJob{
		ID: generateUUID(), EntityType: "menu", EntityID: menuID,
		TargetLocale: "ko", SourceLocale: "en",
		RequestedByMemberID: requestedByMemberID,
		OperationID:         generateUUID(), Status: translationJobStatusRunning,
		RequestArtifactDigest: translation.RequestArtifactDigest(xliff, manifest),
		RequestXLIFF:          xliff, RequestManifest: manifest,
		RequestedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(job).Error)
	return job, &translation.Candidate{}
}

func requireStringValue(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func newTranslationServiceForOGTest(
	db *gorm.DB,
	publisher translationServicePublisher,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	options ...TranslationServiceOption,
) *TranslationService {
	options = append(options, WithTranslationServiceDomainRegistry(testTranslationDomains{}))
	return NewTranslationService(
		db,
		publisher,
		cdnDomain,
		spiceDB,
		&og.Planner{},
		&og.Refresher{},
		options...,
	)
}
