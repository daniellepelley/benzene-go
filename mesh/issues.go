package mesh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// Issue classification vocabulary (mesh.md §4.1) - a closed set assigned by the normative
// precedence table in ClassifyIssue. The other five are never produced outside that table, so
// they stay unexported; ClassificationContractDrift is exported because a collector assigns it
// directly, outside the precedence table (see its own doc).
const (
	classificationValidation   = "validation"
	classificationException    = "exception"
	classificationConfigWiring = "config-wiring"
	classificationDependency   = "dependency"
	classificationUnclassified = "unclassified"

	// ClassificationContractDrift is the issue classification reserved for collector/reader-
	// derived issues (mesh.md §4.1): a descriptor-hash mismatch, schema divergence, or - the
	// case mesh.md §4.2 defines - a trace naming a topic absent from the caller's declared
	// Consumes or the handler's declared Topics. ClassifyIssue's precedence table never
	// produces it; a collector (meshd) assigns it directly when it detects one of these cases.
	ClassificationContractDrift = "contract-drift"
)

// ClassifyIssue assigns an issue classification from a failing invocation's Benzene status and
// captured exception type, by the normative precedence of mesh.md §4.1 (evaluated in order):
//
//  1. bad-request / validation-error -> validation (even with an exception type present - a
//     deserialization or argument failure is still a validation issue);
//  2. an exception type present -> exception (classify by the throw, not the mapped status);
//  3. not-found / unauthorized / forbidden / not-implemented, or an empty status -> config-wiring
//     (a wiring gap);
//  4. service-unavailable / timeout / too-many-requests -> dependency;
//  5. unexpected-error -> exception;
//  6. any other failing status -> unclassified (an honest fallback beats a lying class).
func ClassifyIssue(status, exceptionType string) string {
	switch benzene.Status(status) {
	case benzene.StatusBadRequest, benzene.StatusValidationError:
		return classificationValidation
	}
	if exceptionType != "" {
		return classificationException
	}
	switch benzene.Status(status) {
	case benzene.StatusNotFound, benzene.StatusUnauthorized, benzene.StatusForbidden, benzene.StatusNotImplemented:
		return classificationConfigWiring
	case benzene.StatusServiceUnavailable, benzene.StatusTimeout, benzene.StatusTooManyRequests:
		return classificationDependency
	case benzene.StatusUnexpectedError:
		return classificationException
	}
	if status == "" {
		return classificationConfigWiring
	}
	return classificationUnclassified
}

// maxExemplars bounds the exemplar trace ids kept per fingerprint per window (mesh.md §4.1:
// newest ≤3).
const maxExemplars = 3

// newestExemplars appends the newer ids after the existing ones and keeps the newest maxExemplars.
func newestExemplars(existing, incoming []string) []string {
	combined := append(existing, incoming...)
	if len(combined) > maxExemplars {
		combined = combined[len(combined)-maxExemplars:]
	}
	return combined
}

// IssueFingerprint derives the normative issue fingerprint of mesh.md §4.1: the lowercase hex of
// the first 16 bytes of SHA-256 over the UTF-8 bytes of service|topic|version|classification|
// discriminator (pipe-joined), where version is the empty string when absent and discriminator is
// the exception type when present, else the status. transport is deliberately excluded - the same
// failure over two transports is one issue.
func IssueFingerprint(service, topic, version, classification, discriminator string) string {
	sum := sha256.Sum256([]byte(service + "|" + topic + "|" + version + "|" + classification + "|" + discriminator))
	return hex.EncodeToString(sum[:16])
}

// IssueMiddleware observes every invocation that passes through it and records a failure signature
// to exporter for each unsuccessful outcome (a framework failure, an application-defined failure,
// or an empty status - the wiring gap). Successful invocations produce nothing. Register it
// outermost alongside TraceMiddleware so it sees intercepted invocations too.
//
// A nil exporter returns a pass-through middleware: the issue feed is simply off and the service
// behaves identically to an unmeshed one (mesh.md §4.1: optional on both sides). Recording can
// never fail, slow, or block the invocation it observed - Record holds only a brief lock and the
// exporter's send runs on its own goroutine.
func IssueMiddleware(info ServiceInfo, exporter IssueRecorder) benzene.Middleware {
	if exporter == nil {
		return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
			return next(ctx)
		}
	}
	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		err := next(ctx)
		if issueWorthRecording(ic.Result) {
			status := ""
			if ic.Result != nil {
				status = string(ic.Result.ResultStatus())
			}
			// Link the exemplar to the same trace id the trace feed used for this invocation:
			// TraceMiddleware (registered outer) puts the invocation's span - a fresh id at an
			// entry point, or the adopted inbound one - on the context. Only when no span is
			// present (no trace middleware, or a different ordering) fall back to the inbound
			// traceparent header, which is empty for an edge invocation.
			traceID := ""
			if span, ok := SpanFromContext(ctx); ok {
				traceID = span.TraceID
			} else {
				traceID, _ = parseTraceparent(ic.Headers["traceparent"])
			}
			exporter.Record(IssueOccurrence{
				Topic:   ic.Topic.ID,
				Version: ic.Topic.Version,
				Status:  status,
				TraceID: traceID,
				Service: info.Service,
				At:      time.Now().UTC(),
			})
		}
		return err
	}
}

// issueWorthRecording reports whether an invocation's result is a failure that should be recorded
// as an issue: a nil result (empty status - a wiring gap) or an unsuccessful one. A successful
// result (framework success or application-defined success) is not an issue.
func issueWorthRecording(result benzene.ResultInfo) bool {
	if result == nil {
		return true
	}
	if s, ok := result.(interface{ ResultIsSuccessful() bool }); ok {
		return !s.ResultIsSuccessful()
	}
	return result.ResultStatus().IsFailure()
}

// IssueOccurrence is one failure the middleware observed, before classification/fingerprinting.
// ExceptionType is left empty by the standard middleware: Go's router converts a handler panic to
// a service-unavailable Result before this middleware sees it, so there is no language-native
// thrown type to capture (mesh.md §4.1 makes exceptionType optional for exactly this reason).
type IssueOccurrence struct {
	Service       string
	Topic         string
	Version       string
	Status        string
	ExceptionType string
	TraceID       string
	At            time.Time
}

// IssueRecorder receives the failure occurrences IssueMiddleware observes. Implementations must be
// safe for concurrent use and must not block the invocation.
type IssueRecorder interface {
	Record(occurrence IssueOccurrence)
}

// PushIssueExporterOptions tunes a PushIssueExporter. Zero values mean the defaults.
type PushIssueExporterOptions struct {
	// FlushInterval is how often the accumulated deltas are flushed as one benzene:mesh:issues
	// batch. An empty interval flush still sends an empty batch - the feed's liveness assertion,
	// so a collector can tell a quiet wired service from an unwired one. Default 5s.
	FlushInterval time.Duration
	// Now supplies the clock, for tests. Defaults to time.Now.
	Now func() time.Time
}

// pendingIssue is one fingerprint's accumulation within the current flush window. Count is the
// delta since the last flush (mesh.md §4.1: the wire count is always a delta).
type pendingIssue struct {
	classification string
	topic          string
	version        string
	status         string
	exceptionType  string
	count          int64
	firstSeen      time.Time
	lastSeen       time.Time
	exemplars      []string
}

// PushIssueExporter is the emitter half of the issue feed (mesh.md §4.1): it deduplicates failure
// occurrences by fingerprint at the source (per-occurrence events are never sent), accumulating
// delta counts, and flushes them as one benzene:mesh:issues envelope from a single background
// goroutine. Like the trace feed it is lossy by design - a failed send drops the window's deltas
// (the collector's delta-merge tolerates the loss) - and a nil Sender yields a nil exporter whose
// methods are nil-safe no-ops, degrading to a disabled feed.
type PushIssueExporter struct {
	sender    Sender
	service   string
	interval  time.Duration
	now       func() time.Time
	mu        sync.Mutex
	pending   map[string]*pendingIssue
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewPushIssueExporter starts the background flusher and returns the exporter. A nil sender or an
// empty service returns a nil exporter - usable directly as IssueMiddleware's recorder thanks to
// nil-safe methods, degrading to a disabled issue feed.
func NewPushIssueExporter(sender Sender, service string, options PushIssueExporterOptions) *PushIssueExporter {
	if sender == nil || service == "" {
		return nil
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = 5 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	exporter := &PushIssueExporter{
		sender:   sender,
		service:  service,
		interval: options.FlushInterval,
		now:      options.Now,
		pending:  map[string]*pendingIssue{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go exporter.loop()
	return exporter
}

// Record classifies, fingerprints, and merges one occurrence into the current flush window. Never
// blocks for long (a brief map lock) and is nil-safe, so a (*PushIssueExporter)(nil) wired in
// behaves as the disabled feed.
func (e *PushIssueExporter) Record(occurrence IssueOccurrence) {
	if e == nil {
		return
	}
	classification := ClassifyIssue(occurrence.Status, occurrence.ExceptionType)
	discriminator := occurrence.ExceptionType
	if discriminator == "" {
		discriminator = occurrence.Status
	}
	fingerprint := IssueFingerprint(occurrence.Service, occurrence.Topic, occurrence.Version, classification, discriminator)

	e.mu.Lock()
	defer e.mu.Unlock()
	issue := e.pending[fingerprint]
	if issue == nil {
		issue = &pendingIssue{
			classification: classification,
			topic:          occurrence.Topic,
			version:        occurrence.Version,
			status:         occurrence.Status,
			exceptionType:  occurrence.ExceptionType,
			firstSeen:      occurrence.At,
		}
		e.pending[fingerprint] = issue
	}
	issue.count++
	issue.lastSeen = occurrence.At
	if occurrence.TraceID != "" {
		issue.exemplars = newestExemplars(issue.exemplars, []string{occurrence.TraceID})
	}
}

// Close flushes the current window and stops the background flusher. Safe to call more than once
// and on a nil exporter; call it on shutdown so the tail of the feed isn't lost with the process.
func (e *PushIssueExporter) Close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() { close(e.stop) })
	<-e.done
}

func (e *PushIssueExporter) loop() {
	defer close(e.done)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.Flush()
		case <-e.stop:
			e.Flush()
			return
		}
	}
}

// Flush sends the accumulated deltas as one benzene:mesh:issues batch and resets the window. It
// always sends - an empty window flushes an empty batch, the feed's liveness assertion (mesh.md
// §4.1) - so a collector can distinguish a quiet wired service from an unwired one. The send
// result is deliberately ignored: a dropped batch loses its deltas, lossy by design. Exposed for
// tests and for a caller that wants to flush on its own schedule; nil-safe.
func (e *PushIssueExporter) Flush() {
	if e == nil {
		return
	}
	e.mu.Lock()
	batch := snapshotIssueBatch(e.service, e.pending)
	e.pending = map[string]*pendingIssue{}
	e.mu.Unlock()

	data, _ := json.Marshal(batch)
	e.sender.Send(context.Background(), benzene.NewTopic(TopicIssues), map[string]string{}, data)
}

// snapshotIssueBatch snapshots the pending map into a wire IssueBatch. Caller holds e.mu.
func snapshotIssueBatch(service string, pending map[string]*pendingIssue) IssueBatch {
	batch := IssueBatch{Service: service, Issues: make([]Issue, 0, len(pending))}
	for fingerprint, issue := range pending {
		batch.Issues = append(batch.Issues, Issue{
			Fingerprint:      fingerprint,
			Classification:   issue.classification,
			Service:          service,
			Topic:            issue.topic,
			Version:          issue.version,
			Status:           issue.status,
			ExceptionType:    issue.exceptionType,
			Count:            issue.count,
			FirstSeen:        issue.firstSeen,
			LastSeen:         issue.lastSeen,
			ExemplarTraceIds: issue.exemplars,
		})
	}
	return batch
}
