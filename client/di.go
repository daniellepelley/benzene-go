package client

import (
	benzene "github.com/daniellepelley/benzene-go"
)

// senderKey is the container key the outbound Sender is registered under. It is an unexported
// zero-size type so an application's own registrations can't collide with it, and so the only
// way to register or resolve the framework Sender is through RegisterSender/SenderFromScope.
type senderKey struct{}

// RegisterSender registers sender as the application's outbound Sender (a singleton) so
// handlers can resolve it from their invocation scope with SenderFromScope, rather than each
// handler closing over a concrete client. Registering the Sender in the container - instead of
// capturing it at handler-construction time - is what makes the egress edge swappable in a
// test: benzenetest's WithServices calls RegisterSender again with a
// benzenetest.FakeMessageSender, and last-registration-wins replaces the real client without
// touching the handler or the pipeline - the outbound client is a registered dependency like any
// other, faked at the container edge.
func RegisterSender(container *benzene.Container, sender Sender) {
	benzene.AddSingleton(container, senderKey{}, func(*benzene.Scope) Sender { return sender })
}

// SenderFromScope resolves the Sender registered with RegisterSender, panicking (like every
// required resolution - see scope.go's GetService) if none was registered. A handler that
// publishes calls this with the scope from benzene.ScopeFromContext(ctx).
func SenderFromScope(scope *benzene.Scope) Sender {
	return benzene.GetService[Sender](scope, senderKey{})
}
