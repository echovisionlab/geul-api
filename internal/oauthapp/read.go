package oauthapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ListSignInApps returns every sign-in app with its Hydra settings, ordered
// by name.
func (s *Service) ListSignInApps(ctx context.Context) ([]App, error) {
	if err := s.requireAdmin(ctx, policyv1.OauthSignInApp.List); err != nil {
		return nil, err
	}
	var records []Record
	if err := s.units.Read(ctx, func(store Store) (err error) {
		records, err = store.Apps(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	clients, err := s.listClients(ctx)
	if err != nil {
		return nil, err
	}
	var criteriaIDs []string
	for _, record := range records {
		criteriaIDs = append(criteriaIDs, record.MemberCriteriaIDs...)
	}
	criteria, err := s.criteriaByID(ctx, criteriaIDs)
	if err != nil {
		return nil, err
	}
	apps := make([]App, 0, len(records))
	for _, record := range records {
		settings := missingClientSettings(record)
		if client, ok := clients[record.ClientID]; ok {
			settings = settingsOf(record, client)
		}
		apps = append(apps, appOf(record, settings, pick(criteria, record.MemberCriteriaIDs)))
	}
	slices.SortFunc(apps, func(a, b App) int {
		if order := strings.Compare(strings.ToLower(a.Settings.Name), strings.ToLower(b.Settings.Name)); order != 0 {
			return order
		}
		return strings.Compare(a.ClientID, b.ClientID)
	})
	return apps, nil
}

// GetSignInApp returns one sign-in app.
func (s *Service) GetSignInApp(ctx context.Context, clientID string) (App, error) {
	if err := s.requireAdmin(ctx, policyv1.OauthSignInApp.List); err != nil {
		return App{}, err
	}
	var record Record
	if err := s.units.Read(ctx, func(store Store) (err error) {
		record, err = store.App(ctx, strings.TrimSpace(clientID))
		return err
	}); err != nil {
		return App{}, appNotFound(err, clientID)
	}
	return s.app(ctx, record)
}

// app reads the Hydra settings and Member criteria of record.
func (s *Service) app(ctx context.Context, record Record) (App, error) {
	settings := missingClientSettings(record)
	client, err := s.clients.Get(ctx, record.ClientID)
	switch {
	case err == nil:
		settings = settingsOf(record, client)
	case !errors.Is(err, ErrClientNotFound):
		return App{}, hydraUnavailable(err)
	}
	criteria, err := s.criteriaByID(ctx, record.MemberCriteriaIDs)
	if err != nil {
		return App{}, err
	}
	return appOf(record, settings, pick(criteria, record.MemberCriteriaIDs)), nil
}

func (s *Service) listClients(ctx context.Context) (map[string]Client, error) {
	clients, err := s.clients.List(ctx)
	if err != nil {
		return nil, hydraUnavailable(err)
	}
	byID := make(map[string]Client, len(clients))
	for _, client := range clients {
		byID[client.ID] = client
	}
	return byID, nil
}

func (s *Service) criteriaByID(ctx context.Context, ids []string) (map[string]CriteriaSummary, error) {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	byID := make(map[string]CriteriaSummary, len(ids))
	if len(ids) == 0 {
		return byID, nil
	}
	summaries, err := s.eligibility.CriteriaSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}
	return byID, nil
}

// pick returns the summaries of ids in order; a criteria that no longer
// exists is left out.
func pick(byID map[string]CriteriaSummary, ids []string) []CriteriaSummary {
	result := make([]CriteriaSummary, 0, len(ids))
	for _, id := range ids {
		if summary, ok := byID[id]; ok {
			result = append(result, summary)
		}
	}
	return result
}

// requireAdmin checks an administrator-only read.
func (s *Service) requireAdmin(ctx context.Context, descriptor func() (policyv1.Can, error)) error {
	can, err := descriptor()
	if err != nil {
		return fmt.Errorf("build authorization check: %w", err)
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

func appNotFound(err error, clientID string) error {
	if errors.Is(err, ErrAppNotFound) {
		return domainerr.ResourceNotFound("sign-in app", clientID)
	}
	return err
}

func hydraUnavailable(err error) error {
	return domainerr.DependencyUnavailable("Hydra").WithCause(err)
}
