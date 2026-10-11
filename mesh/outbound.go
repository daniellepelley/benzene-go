package mesh

import "github.com/daniellepelley/benzene-go/contract"

// OutboundRegistry is contract.OutboundRegistry: the service's event and send declarations
// (contract-document.md §3), which the descriptor's Produces list reads alongside the Contract
// Document's events and sends. Declare with contract.RegisterEvent and contract.RegisterSend.
//
// Deprecated: use contract.OutboundRegistry. The alias stays for one release, until the
// descriptor is folded into the Contract Document.
type OutboundRegistry = contract.OutboundRegistry

// NewOutboundRegistry returns an empty OutboundRegistry.
//
// Deprecated: use contract.NewOutboundRegistry.
func NewOutboundRegistry() *OutboundRegistry { return contract.NewOutboundRegistry() }
