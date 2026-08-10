// Package clienthealthcheck is the consumer-side dependency health check, matching
// Benzene.Clients.HealthChecks. A ServiceCheck probes a downstream Benzene provider through an
// outbound client.Sender and reports the *contract relationship* with that provider, not the
// provider's transient internal health:
//
//   - provider unreachable / serves no descriptor  failed
//   - reachable, contract hash matches ............ ok
//   - reachable, contract hash has drifted ........ warning (degraded, does not flip the caller's health)
//   - reachable, drift-detection not configured ... ok  (reachability only)
//   - reachable, descriptor carries no hash ....... ok  (drift could not be assessed; noted in Data)
//
// Reachability and the contract hash are both read from the provider's reserved benzene:mesh
// descriptor, which mesh.Middleware serves with a success status *unconditionally* - a reachable but
// transiently-unhealthy provider still serves its descriptor. This is deliberately NOT the
// benzene:healthcheck topic: that returns a failure status when the provider's own checks are merely
// failing, and the envelope transport drops a failure response's body (httpclient.toResult), so a
// healthcheck probe could not tell "unhealthy" from "down" - and coupling this check to the
// provider's transient health is exactly what it must not do (only an unreachable provider, or one
// that serves no contract to verify, is our failure). This diverges from the .NET reference, which
// keys reachability on its generated client's health-call payload; Go's descriptor carries the hash
// and is health-independent, so it is the faithful signal here.
//
// A provider that is up but does not serve a benzene:mesh descriptor (profile R6) has no discoverable
// contract, so this check cannot verify it and reports failed with the observed status - provision
// the descriptor (a Benzene Cloud Service must). Register the check on a *contracts* diagnostic
// surface, NOT a liveness or readiness probe: it calls a downstream service, so a probe that included
// it would let one struggling dependency restart or de-route otherwise-healthy instances.
//
// It lives in its own package (not in healthcheck) so healthcheck keeps its net/net-http-only
// footprint - this check additionally needs the outbound client.Sender and the mesh descriptor,
// exactly as .NET keeps it in a distinct Benzene.Clients.HealthChecks package.
package clienthealthcheck

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/healthcheck"
	"github.com/daniellepelley/benzene-go/mesh"
)

// ServiceCheck is a healthcheck.Check that probes one downstream Benzene provider via an outbound
// client.Sender. See the package doc for the verdict rules.
type ServiceCheck struct {
	name         string
	sender       client.Sender
	expectedHash string // the contract hash the consumer was built against; empty = reachability only
}

// Option configures a ServiceCheck.
type Option func(*ServiceCheck)

// WithExpectedContractHash enables contract-drift detection: the check compares the provider's live
// descriptor hash (mesh.md §5.1, from its reserved benzene:mesh topic) against expected - the hash
// the consumer was generated/built against - and reports a warning when they differ. Without it the
// check only verifies reachability (that the provider serves its descriptor).
func WithExpectedContractHash(expected string) Option {
	return func(c *ServiceCheck) { c.expectedHash = expected }
}

// New builds a ServiceCheck named after the downstream service, probing it through sender (an
// outbound client for that service - e.g. httpclient.NewClient(providerEnvelopeURL), optionally
// decorated with correlation/trace/retry).
func New(serviceName string, sender client.Sender, opts ...Option) *ServiceCheck {
	c := &ServiceCheck{name: serviceName, sender: sender}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name identifies the check in the aggregate health response.
func (c *ServiceCheck) Name() string { return c.name }

// Check probes the provider's reserved benzene:mesh descriptor for reachability and, when a contract
// hash was configured, for contract drift.
func (c *ServiceCheck) Check(ctx context.Context) healthcheck.CheckResult {
	hash, reachable, answered := c.probeDescriptor(ctx)
	if !reachable {
		return c.result(healthcheck.StatusFailed, map[string]any{
			"service":   c.name,
			"reachable": false,
			"reason":    "provider descriptor did not round-trip (unreachable, or no benzene:mesh descriptor served); status " + string(answered),
		})
	}

	if c.expectedHash == "" {
		return c.result(healthcheck.StatusOk, map[string]any{"service": c.name, "reachable": true})
	}

	if hash == "" {
		// Reachable, but the descriptor carries no readable hash - drift can't be assessed. Reachability
		// passed, so this is ok, not a failure; the gap is recorded rather than hidden.
		return c.result(healthcheck.StatusOk, map[string]any{
			"service":       c.name,
			"reachable":     true,
			"driftAssessed": false,
		})
	}

	if hash != c.expectedHash {
		return c.result(healthcheck.StatusWarning, map[string]any{
			"service":      c.name,
			"reachable":    true,
			"contractHash": hash,
			"expectedHash": c.expectedHash,
			"drifted":      true,
		})
	}
	return c.result(healthcheck.StatusOk, map[string]any{
		"service":      c.name,
		"reachable":    true,
		"contractHash": hash,
		"drifted":      false,
	})
}

// probeDescriptor fetches the downstream's reserved benzene:mesh descriptor. reachable is true when
// the topic round-trips with a success status (the provider is up and serving its descriptor,
// independent of its own health - mesh.Middleware answers Ok unconditionally); a transport failure or
// a not-found/failure status means unreachable-or-not-served. hash is the descriptor's
// DescriptorHash, extracted best-effort (empty when the response has no body, an unparseable body, or
// a descriptor with no hash - all "reachable, but drift not assessable"). answered is the observed
// status, surfaced in a failure's reason.
func (c *ServiceCheck) probeDescriptor(ctx context.Context) (hash string, reachable bool, answered benzene.Status) {
	desc := c.sender.Send(ctx, benzene.NewTopic(mesh.TopicID), nil, []byte("{}"))
	if !desc.IsSuccessful() {
		return "", false, desc.ResultStatus()
	}
	if desc.Payload != nil {
		var descriptor mesh.Descriptor
		if json.Unmarshal(*desc.Payload, &descriptor) == nil {
			hash = descriptor.DescriptorHash
		}
	}
	return hash, true, desc.ResultStatus()
}

func (c *ServiceCheck) result(status healthcheck.Status, data map[string]any) healthcheck.CheckResult {
	return healthcheck.CheckResult{Status: status, Type: c.name, Data: data}
}
