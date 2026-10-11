// Package clienthealthcheck is the consumer-side dependency health check, matching
// Benzene.Clients.HealthChecks. A ServiceCheck probes a downstream Benzene provider through an
// outbound client.Sender and reports the *contract relationship* with that provider, not the
// provider's transient internal health:
//
//   - provider unreachable / serves no contract ... failed
//   - reachable, contract hash matches ............ ok
//   - reachable, contract hash has drifted ........ warning (degraded, does not flip the caller's health)
//   - reachable, drift-detection not configured ... ok  (reachability only)
//   - reachable, document carries no hash ......... ok  (drift could not be assessed; noted in Data)
//
// Reachability and the contract hash are both read from the provider's reserved benzene:spec topic,
// which answers with its Contract Document (contract-document.md §7) - the served contractHash is the
// value to compare against, so the check computes no hash of its own. contract.Middleware answers
// that topic with a success status *unconditionally*, so a reachable but transiently-unhealthy
// provider still serves its contract. This is deliberately NOT the benzene:healthcheck topic: its
// report carries the provider's own transient verdict (isHealthy), and coupling this check to the
// provider's transient health is exactly what it must not do (only an unreachable provider, or one
// that serves no contract to verify, is our failure).
//
// The expected hash is the contractHash a consumer was built against - a generated client's
// ContractHash constant - and compares directly with the served value when both cover the same
// projection (contract-document.md §6.4): a default (no include-list) generated client embeds the
// whole-service contractHash, which is exactly what the provider serves.
//
// A provider that is up but does not serve benzene:spec has no discoverable contract, so this check
// cannot verify it and reports failed with the observed status - provision it (a Benzene Cloud
// Service must, for R5). Register the check on a *contracts* diagnostic surface, NOT a liveness or
// readiness probe: it calls a downstream service, so a probe that included it would let one
// struggling dependency restart or de-route otherwise-healthy instances.
//
// It lives in its own package (not in healthcheck) so healthcheck keeps its net/net-http-only
// footprint - this check additionally needs the outbound client.Sender and the contract package,
// exactly as .NET keeps it in a distinct Benzene.Clients.HealthChecks package.
package clienthealthcheck

import (
	"context"
	"encoding/json"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/client"
	"github.com/daniellepelley/benzene-go/contract"
	"github.com/daniellepelley/benzene-go/healthcheck"
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

// WithExpectedContractHash enables contract-drift detection: the check compares the contractHash the
// provider serves on its reserved benzene:spec topic against expected - the hash the consumer was
// generated/built against (a generated client's ContractHash) - and reports a warning when they
// differ. Without it the check only verifies reachability (that the provider serves its contract).
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

// Check probes the provider's reserved benzene:spec topic for reachability and, when a contract hash
// was configured, for contract drift.
func (c *ServiceCheck) Check(ctx context.Context) healthcheck.CheckResult {
	hash, reachable, answered := c.probeContract(ctx)
	if !reachable {
		return c.result(healthcheck.StatusFailed, map[string]any{
			"service":   c.name,
			"reachable": false,
			"reason":    "provider contract did not round-trip (unreachable, or no benzene:spec served); status " + string(answered),
		})
	}

	if c.expectedHash == "" {
		return c.result(healthcheck.StatusOk, map[string]any{"service": c.name, "reachable": true})
	}

	if hash == "" {
		// Reachable, but the document carries no readable hash - drift can't be assessed. Reachability
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

// probeContract asks the downstream's reserved benzene:spec topic for its Contract Document (the
// default request: the benzene document as JSON). reachable is true when the topic round-trips with
// a success status (the provider is up and serving its contract, independent of its own health -
// contract.Middleware answers Ok unconditionally); a transport failure or a not-found/failure status
// means unreachable-or-not-served. hash is the served contractHash, read best-effort (empty when the
// response has no body, an unparseable body, or a document with no hash - all "reachable, but drift
// not assessable"). answered is the observed status, surfaced in a failure's reason.
func (c *ServiceCheck) probeContract(ctx context.Context) (hash string, reachable bool, answered benzene.Status) {
	spec := c.sender.Send(ctx, benzene.NewTopic(contract.TopicID), nil, []byte("{}"))
	if !spec.IsSuccessful() {
		return "", false, spec.ResultStatus()
	}
	if spec.Payload != nil {
		var document struct {
			ContractHash string `json:"contractHash"`
		}
		if json.Unmarshal(*spec.Payload, &document) == nil {
			hash = document.ContractHash
		}
	}
	return hash, true, spec.ResultStatus()
}

func (c *ServiceCheck) result(status healthcheck.Status, data map[string]any) healthcheck.CheckResult {
	return healthcheck.CheckResult{Status: status, Type: c.name, Data: data}
}
