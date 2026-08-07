package meshd

import (
	"testing"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
	"github.com/daniellepelley/benzene-go/mesh"
)

func ts(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func issueBatch(service string, issues ...mesh.Issue) mesh.IssueBatch {
	return mesh.IssueBatch{Service: service, Issues: issues}
}

func containsFeed(feeds []string, feed string) bool {
	for _, f := range feeds {
		if f == feed {
			return true
		}
	}
	return false
}

func fleetIssue(t *testing.T, c *Collector, fingerprint string) (IssueView, bool) {
	t.Helper()
	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	for _, issue := range fleet.Issues {
		if issue.Fingerprint == fingerprint {
			return issue, true
		}
	}
	return IssueView{}, false
}

func TestCollector_IssueBatchRequiresService(t *testing.T) {
	c := newTestCollector(t)
	status, _ := invoke[Ack](t, c, mesh.TopicIssues, issueBatch(""))
	if status != benzene.StatusBadRequest {
		t.Errorf("status = %q, want %q for a batch with no service", status, benzene.StatusBadRequest)
	}
}

func TestCollector_EmptyIssueBatchIsLiveness(t *testing.T) {
	c := newTestCollector(t)
	status, ack := invoke[Ack](t, c, mesh.TopicIssues, issueBatch("orders"))
	if status != benzene.StatusOk || ack.Accepted != 0 {
		t.Errorf("(status, accepted) = (%q, %d), want (ok, 0)", status, ack.Accepted)
	}
}

func TestCollector_IssuesMergeByFingerprint(t *testing.T) {
	c := newTestCollector(t)
	base := mesh.Issue{
		Fingerprint: "fp1", Classification: "exception", Service: "orders", Topic: "order:create",
		Status: "service-unavailable", ExceptionType: "System.TimeoutException",
	}
	first := base
	first.Count, first.FirstSeen, first.LastSeen, first.ExemplarTraceIds = 2, ts("2026-07-25T09:00:00Z"), ts("2026-07-25T09:05:00Z"), []string{"trace-1"}
	second := base
	second.Count, second.FirstSeen, second.LastSeen, second.ExemplarTraceIds = 3, ts("2026-07-25T09:10:00Z"), ts("2026-07-25T09:15:00Z"), []string{"trace-2"}

	if _, ack := invoke[Ack](t, c, mesh.TopicIssues, issueBatch("orders", first)); ack.Accepted != 1 {
		t.Fatalf("first accepted = %d, want 1", ack.Accepted)
	}
	if _, ack := invoke[Ack](t, c, mesh.TopicIssues, issueBatch("orders", second)); ack.Accepted != 1 {
		t.Fatalf("second accepted = %d, want 1", ack.Accepted)
	}

	issue, ok := fleetIssue(t, c, "fp1")
	if !ok {
		t.Fatal("fp1 not found on the fleet view")
	}
	if issue.Count != 5 {
		t.Errorf("count = %d, want 5 (2 + 3 delta merge)", issue.Count)
	}
	if len(issue.ExemplarTraceIds) != 2 || issue.ExemplarTraceIds[0] != "trace-1" || issue.ExemplarTraceIds[1] != "trace-2" {
		t.Errorf("exemplars = %v, want [trace-1 trace-2]", issue.ExemplarTraceIds)
	}
	if !issue.FirstSeen.Equal(ts("2026-07-25T09:00:00Z")) {
		t.Errorf("firstSeen = %v, want the earliest", issue.FirstSeen)
	}
	if !issue.LastSeen.Equal(ts("2026-07-25T09:15:00Z")) {
		t.Errorf("lastSeen = %v, want the latest", issue.LastSeen)
	}
}

func TestCollector_IssuesFromSeveralServicesAreSortedByServiceThenFingerprint(t *testing.T) {
	c := newTestCollector(t)
	// orders carries two fingerprints (exercises the same-service fingerprint tiebreak); billing
	// one (exercises the service ordering).
	invoke[Ack](t, c, mesh.TopicIssues, issueBatch("orders",
		mesh.Issue{Fingerprint: "zzzz", Service: "orders", Topic: "t", Status: "service-unavailable", Count: 1},
		mesh.Issue{Fingerprint: "mmmm", Service: "orders", Topic: "t", Status: "service-unavailable", Count: 1}))
	invoke[Ack](t, c, mesh.TopicIssues, issueBatch("billing",
		mesh.Issue{Fingerprint: "aaaa", Service: "billing", Topic: "t", Status: "service-unavailable", Count: 1}))

	_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
	if len(fleet.Issues) != 3 {
		t.Fatalf("Issues = %+v, want three", fleet.Issues)
	}
	// Sorted by service then fingerprint: billing/aaaa, orders/mmmm, orders/zzzz.
	got := [3][2]string{
		{fleet.Issues[0].Service, fleet.Issues[0].Fingerprint},
		{fleet.Issues[1].Service, fleet.Issues[1].Fingerprint},
		{fleet.Issues[2].Service, fleet.Issues[2].Fingerprint},
	}
	want := [3][2]string{{"billing", "aaaa"}, {"orders", "mmmm"}, {"orders", "zzzz"}}
	if got != want {
		t.Errorf("issue order = %v, want %v", got, want)
	}
}

func TestCollector_InvalidIssueEntriesAreSkipped(t *testing.T) {
	c := newTestCollector(t)
	invalid := mesh.Issue{Fingerprint: "", Classification: "validation", Service: "orders", Topic: "order:create", Status: "bad-request", Count: 1}
	valid := mesh.Issue{Fingerprint: "fp-valid", Classification: "validation", Service: "orders", Topic: "order:create", Status: "bad-request", Count: 1}

	_, ack := invoke[Ack](t, c, mesh.TopicIssues, issueBatch("orders", invalid, valid))
	if ack.Accepted != 1 {
		t.Errorf("accepted = %d, want 1 (the empty-fingerprint entry is skipped, not rejected)", ack.Accepted)
	}
	if _, ok := fleetIssue(t, c, ""); ok {
		t.Error("an empty-fingerprint issue must not appear on the fleet view")
	}
	if _, ok := fleetIssue(t, c, "fp-valid"); !ok {
		t.Error("the valid issue should appear on the fleet view")
	}
}

func TestCollector_ExemplarsKeepNewestThree(t *testing.T) {
	c := newTestCollector(t)
	fp := "fp-ex"
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		issue := mesh.Issue{Fingerprint: fp, Service: "orders", Topic: "t", Status: "service-unavailable", Count: 1, ExemplarTraceIds: []string{id}}
		invoke[Ack](t, c, mesh.TopicIssues, issueBatch("orders", issue))
	}
	issue, ok := fleetIssue(t, c, fp)
	if !ok {
		t.Fatal("fp-ex not found")
	}
	want := []string{"t2", "t3", "t4"}
	if len(issue.ExemplarTraceIds) != 3 {
		t.Fatalf("exemplars = %v, want the newest 3", issue.ExemplarTraceIds)
	}
	for i, id := range want {
		if issue.ExemplarTraceIds[i] != id {
			t.Errorf("exemplars = %v, want %v (oldest dropped)", issue.ExemplarTraceIds, want)
		}
	}
	if issue.Count != 4 {
		t.Errorf("count = %d, want 4", issue.Count)
	}
}

func TestCollector_IssuesMissingFeedFlaggedOnlyWhenFailureNeedsExplaining(t *testing.T) {
	missingIssues := func(c *Collector, service string) bool {
		_, fleet := invoke[FleetView](t, c, mesh.TopicQueryFleet, struct{}{})
		for _, s := range fleet.Services {
			if s.Service == service {
				return containsFeed(s.MissingFeeds, "issues")
			}
		}
		t.Fatalf("service %q not on fleet view", service)
		return false
	}

	// A failing trace with no issue feed -> "issues" is flagged (the failure is unexplained).
	c := newTestCollector(t)
	invoke[Ack](t, c, mesh.TopicTraces, mesh.TraceBatch{Events: []mesh.TraceEvent{
		event("trace-1", "span-1", "", "orders", "order:create", "service-unavailable", 5),
	}})
	if !missingIssues(c, "orders") {
		t.Error(`"issues" should be flagged missing when a service has failures but no issue feed`)
	}
	// After any issue batch (even empty), the feed is live -> not flagged.
	invoke[Ack](t, c, mesh.TopicIssues, issueBatch("orders"))
	if missingIssues(c, "orders") {
		t.Error(`"issues" should not be flagged once the issue feed is live`)
	}

	// A service with only successful traffic is never reduced by the issue feed's absence.
	healthy := newTestCollector(t)
	invoke[Ack](t, healthy, mesh.TopicTraces, mesh.TraceBatch{Events: []mesh.TraceEvent{
		event("trace-2", "span-2", "", "billing", "bill:charge", "ok", 5),
	}})
	if missingIssues(healthy, "billing") {
		t.Error(`"issues" should not be flagged for a service with no failures`)
	}
}
