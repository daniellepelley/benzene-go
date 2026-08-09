// Package clienthealthcheck is the consumer-side dependency health check, matching
// Benzene.Clients.HealthChecks. A ServiceCheck probes a downstream Benzene provider through an
// outbound client.Sender and reports the *contract relationship* with that provider, not the
// provider's transient internal health:
//
//   - unreachable provider ..................... failed
//   - reachable, contract hash matches ......... ok
//   - reachable, contract hash has drifted ..... warning (degraded, does not flip the caller's health)
//   - reachable, drift-detection not configured  ok  (reachability only)
//   - reachable, provider exposes no descriptor  ok  (drift could not be assessed; noted in Data)
//
// Register it on a *contracts* diagnostic surface, NOT a liveness or readiness probe. It calls a
// downstream service, so a probe that included it would let one struggling dependency restart or
// de-route otherwise-healthy instances - the anti-pattern the contracts/probe separation exists to
// prevent. Its verdicts deliberately track the contract, not the provider's transient health (which
// is the provider's own readiness concern): drift is a warning (degraded-but-not-fatal), and only an
// unreachable provider is a failure.
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
// descriptor hash (mesh.md §5.1, read from its reserved benzene:mesh topic) against expected - the
// hash the consumer was generated/built against - and reports a warning when they differ. Without it
// the check only verifies reachability.
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

// Check probes the provider: reachability via its reserved benzene:healthcheck topic, then - when a
// contract hash was configured - contract drift via its reserved benzene:mesh descriptor.
func (c *ServiceCheck) Check(ctx context.Context) healthcheck.CheckResult {
	reach := c.sender.Send(ctx, benzene.NewTopic(healthcheck.ReservedTopic), nil, []byte("{}"))
	if !reach.IsSuccessful() {
		return c.result(healthcheck.StatusFailed, map[string]any{
			"service":   c.name,
			"reachable": false,
			"reason":    "downstream healthcheck returned status " + string(reach.ResultStatus()),
		})
	}

	if c.expectedHash == "" {
		return c.result(healthcheck.StatusOk, map[string]any{"service": c.name, "reachable": true})
	}

	liveHash, ok := c.providerHash(ctx)
	if !ok {
		// Reachable, but the descriptor feed is not provisioned or not readable - the profile allows a
		// service to omit it (cloud-service-profile.md §4), so drift can't be assessed. Reachability
		// passed, so this is ok, not a failure; the gap is recorded rather than hidden.
		return c.result(healthcheck.StatusOk, map[string]any{
			"service":       c.name,
			"reachable":     true,
			"driftAssessed": false,
		})
	}

	if liveHash != c.expectedHash {
		return c.result(healthcheck.StatusWarning, map[string]any{
			"service":      c.name,
			"reachable":    true,
			"contractHash": liveHash,
			"expectedHash": c.expectedHash,
			"drifted":      true,
		})
	}
	return c.result(healthcheck.StatusOk, map[string]any{
		"service":      c.name,
		"reachable":    true,
		"contractHash": liveHash,
		"drifted":      false,
	})
}

// providerHash fetches the downstream's descriptor hash from its reserved benzene:mesh topic. ok is
// false when the descriptor is unreachable, unparseable, or carries no hash.
func (c *ServiceCheck) providerHash(ctx context.Context) (hash string, ok bool) {
	desc := c.sender.Send(ctx, benzene.NewTopic(mesh.TopicID), nil, []byte("{}"))
	if !desc.IsSuccessful() || desc.Payload == nil {
		return "", false
	}
	var descriptor mesh.Descriptor
	if err := json.Unmarshal(*desc.Payload, &descriptor); err != nil || descriptor.DescriptorHash == "" {
		return "", false
	}
	return descriptor.DescriptorHash, true
}

func (c *ServiceCheck) result(status healthcheck.Status, data map[string]any) healthcheck.CheckResult {
	return healthcheck.CheckResult{Status: status, Type: c.name, Data: data}
}
