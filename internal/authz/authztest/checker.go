// Package authztest provides the authorization checker unit tests use in
// place of SpiceDB. A Checker answers each generated Can descriptor with the
// rule a test set, records every check and relationship batch, and serves the
// same rules over gRPC for a use case that still takes *auth.SpiceDBClient.
// It knows no schema: whether admin implies edit is the test's rule, and the
// SpiceDB contract tests keep the schema semantics.
package authztest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Check is one authorization question the Checker answered.
type Check struct {
	IdentityID   string
	ResourceType string
	ResourceID   string
	Permission   string
}

// Of returns the Check of can asked for identityID, to compare with Checks.
func Of(identityID string, can policyv1.Can, err error) Check {
	mustBeValid(can, err)
	return Check{
		IdentityID: identityID, ResourceType: can.Resource().Type(),
		ResourceID: can.Resource().ID(), Permission: can.Action().Permission(),
	}
}

type rule struct {
	identityID string // empty for every identity
	engineKey  string
}

// Checker implements authz.AuthorizationDecisionChecker, CheckActorCan and
// authzmutation.RelationshipWriter. The zero value denies every check and
// accepts every relationship write.
type Checker struct {
	mu          sync.Mutex
	allowAll    bool
	rules       map[rule]bool
	err         error
	writeErr    error
	checks      []Check
	applied     [][]policyv1.RelationshipMutation
	compensated [][]policyv1.RelationshipMutation
}

// AllowAll returns a Checker that allows every check a rule does not deny.
func AllowAll() *Checker {
	return &Checker{allowAll: true}
}

// Allow allows can for every identity. It takes a generated descriptor's two
// results directly: Allow(policyv1.Post.Edit(id)).
func (c *Checker) Allow(can policyv1.Can, err error) *Checker {
	return c.set("", can, err, true)
}

// Deny denies can for every identity, overriding AllowAll and an earlier
// Allow, as when a test demotes the caller between two requests.
func (c *Checker) Deny(can policyv1.Can, err error) *Checker {
	return c.set("", can, err, false)
}

// For scopes the next Allow or Deny to identityID:
// checker.For(owner).Allow(policyv1.Post.Edit(id)).
func (c *Checker) For(identityID string) Identity {
	return Identity{checker: c, identityID: identityID}
}

// Identity sets rules for one identity; they take precedence over the
// rules for every identity.
type Identity struct {
	checker    *Checker
	identityID string
}

// Allow allows can for the identity.
func (i Identity) Allow(can policyv1.Can, err error) *Checker {
	return i.checker.set(i.identityID, can, err, true)
}

// Deny denies can for the identity.
func (i Identity) Deny(can policyv1.Can, err error) *Checker {
	return i.checker.set(i.identityID, can, err, false)
}

func (c *Checker) set(identityID string, can policyv1.Can, err error, allowed bool) *Checker {
	mustBeValid(can, err)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rules == nil {
		c.rules = map[rule]bool{}
	}
	key := rule{identityID: identityID, engineKey: can.EngineKey()}
	if identityID == "" {
		// A rule for every identity replaces the identity rules of can.
		for existing := range c.rules {
			if existing.engineKey == key.engineKey {
				delete(c.rules, existing)
			}
		}
	}
	c.rules[key] = allowed
	return c
}

// Fail makes every later check fail with err, as an unreachable SpiceDB
// does; nil restores the rules.
func (c *Checker) Fail(err error) *Checker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
	return c
}

// FailWrites makes every later relationship write fail with err.
func (c *Checker) FailWrites(err error) *Checker {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeErr = err
	return c
}

// Can implements authz.AuthorizationDecisionChecker.
func (c *Checker) Can(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
	if !decision.Valid() {
		return false, fmt.Errorf("authorization decision is invalid")
	}
	return c.answer(Check{
		IdentityID:   decision.Actor().AccountIdentityID(),
		ResourceType: decision.Resource().Type(),
		ResourceID:   decision.Resource().ID(),
		Permission:   decision.Action().Permission(),
	})
}

// CheckActorCan answers an internal actor capability check by the same rules.
func (c *Checker) CheckActorCan(_ context.Context, actor policyv1.Actor, can policyv1.Can) (bool, error) {
	if !actor.Valid() || !can.Valid() {
		return false, fmt.Errorf("actor capability check is invalid")
	}
	return c.answer(Of(actor.AccountIdentityID(), can, nil))
}

func (c *Checker) answer(check Check) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks = append(c.checks, check)
	if c.err != nil {
		return false, c.err
	}
	engineKey := check.ResourceType + "\x00" + check.ResourceID + "\x00" + check.Permission
	if allowed, ok := c.rules[rule{identityID: check.IdentityID, engineKey: engineKey}]; ok {
		return allowed, nil
	}
	if allowed, ok := c.rules[rule{engineKey: engineKey}]; ok {
		return allowed, nil
	}
	return c.allowAll, nil
}

// Checks returns every check answered so far, in order.
func (c *Checker) Checks() []Check {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.checks)
}

// ApplyRelationshipsExpecting implements authzmutation.RelationshipWriter.
func (c *Checker) ApplyRelationshipsExpecting(_ context.Context, apply, _ []policyv1.RelationshipMutation) (auth.ZedToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return auth.ZedToken{}, c.writeErr
	}
	c.applied = append(c.applied, slices.Clone(apply))
	return auth.ZedToken{}, nil
}

// CompensateRelationshipsExpecting implements authzmutation.RelationshipWriter.
func (c *Checker) CompensateRelationshipsExpecting(_ context.Context, restore, _ []policyv1.RelationshipMutation) (auth.ZedToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return auth.ZedToken{}, c.writeErr
	}
	c.compensated = append(c.compensated, slices.Clone(restore))
	return auth.ZedToken{}, nil
}

// Applied returns every relationship batch written so far, in order.
func (c *Checker) Applied() [][]policyv1.RelationshipMutation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.applied)
}

// Compensated returns every relationship batch restored on rollback, in order.
func (c *Checker) Compensated() [][]policyv1.RelationshipMutation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.compensated)
}

func mustBeValid(can policyv1.Can, err error) {
	if err != nil {
		panic(fmt.Sprintf("authztest: build Can descriptor: %v", err))
	}
	if !can.Valid() {
		panic("authztest: Can descriptor is invalid")
	}
}
