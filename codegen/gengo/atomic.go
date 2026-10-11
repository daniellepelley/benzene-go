package gengo

import (
	"fmt"
	"strings"

	"github.com/daniellepelley/benzene-go/codegen/contractdoc"
)

// AtomicOptions configures GenerateAtomicClients: which topics get their own self-contained
// (§5.3 topic-scoped) client, mirroring AtomicClientSdkBuilder/ClientSdkOptions.
type AtomicOptions struct {
	// Topics is the include-list (contract-document.md §5.2): when non-empty, only these topics
	// get an atomic client; empty means every requests[] topic does.
	Topics []string
}

// AtomicClient is one topic's self-contained generated client (contract-document.md §5.3): its
// own package, containing only that topic's reachable component schemas.
type AtomicClient struct {
	// Topic is the topic this client was generated for.
	Topic string
	// Dir is the output subdirectory this client's files belong in (also its Go package name) -
	// every atomic client gets its own directory so two independently-generated topic clients
	// can never collide on a shared DTO name, mirroring AtomicClientSdkBuilder's per-client
	// namespace/folder.
	Dir string
	// PackageName is this client's Go package name (equal to Dir).
	PackageName string
	// ClientName is the exported Go type name prefix (ClientName+"Client",
	// "New"+ClientName+"Client"), derived from Topic via TopicMethodName (the non-reversed
	// convention - AtomicClientSdkBuilder's default TopicMethodName clientNameFormatter).
	ClientName string
	// Files is this client's generated source files (types.go, client.go).
	Files []GeneratedFile
}

// GenerateAtomicClients builds one AtomicClient per topic doc's ApplyScope(opts) admits (§5.2),
// each a full §5.3 topic-scoped projection of doc (schema closure narrowed to just that topic, no
// events or sends, and the projection's own contract hash).
func GenerateAtomicClients(doc *contractdoc.Document, opts AtomicOptions) ([]AtomicClient, error) {
	scoped, err := contractdoc.ApplyScope(doc, contractdoc.ScopeOptions{Topics: opts.Topics})
	if err != nil {
		return nil, err
	}

	var clients []AtomicClient
	for _, r := range scoped.Requests() {
		topic := contractdoc.RequestTopic(r)

		// Project from the ORIGINAL (unscoped) document, not `scoped`: the closure walk must see
		// the full schema catalogue to resolve every $ref this one topic's request/response
		// reaches, exactly as AtomicClientSdkBuilder.ReachableSchemas walks the full
		// document.Components.Schemas, not an already-filtered subset.
		topicDoc, err := contractdoc.TopicScopedProjection(doc, topic)
		if err != nil {
			return nil, err
		}

		clientName := TopicMethodName(topic)
		pkgName := strings.ToLower(clientName)
		if err := ValidateGoIdentifier(pkgName); err != nil {
			return nil, fmt.Errorf("gengo: topic %q derives an invalid package name %q: %w", topic, pkgName, err)
		}

		files, err := GenerateServiceClient(topicDoc, ServiceOptions{
			ServiceName: clientName,
			PackageName: pkgName,
		})
		if err != nil {
			return nil, fmt.Errorf("gengo: topic %q: %w", topic, err)
		}

		clients = append(clients, AtomicClient{
			Topic:       topic,
			Dir:         pkgName,
			PackageName: pkgName,
			ClientName:  clientName,
			Files:       files,
		})
	}
	return clients, nil
}
