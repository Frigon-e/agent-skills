package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func absenceReview() (*gateSubject, []reviewCheck, []reviewAnswer, reviewPolicy) {
	s, cs, as, _, p := reducerFixture("go")
	as[0].Judgment = reviewJudgmentNew
	as[0].LookupIDs = []string{p.Packet.Lookups[0].ID}
	as[0].EvidenceIDs = nil
	as[0].Support.EvidenceIDs = nil
	as[0].Support.Rationale = "The complete in-scope search found no implementation"
	as[0].Rationale = as[0].Support.Rationale
	p.Packet.Evidence = nil
	p.CurrentHashes = nil
	p.Packet.Lookups[0].Status = "empty"
	p.Packet.Lookups[0].Counts.Total = evidenceTestPtr(0)
	return s, cs, as, p
}

func TestReduceAbsenceUsesCompletedZeroResultLookup(t *testing.T) {
	s, cs, as, p := absenceReview()
	if err := p.Packet.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := as[0].Validate(cs[0].Item, p.Packet, s, nil); err != nil {
		t.Fatal(err)
	}
	r := ReduceReview(s, cs, as, nil, p)
	if r.State != "pass" || r.Completion.Completed != 1 {
		t.Fatalf("zero-result search could not support new behavior: %+v", r)
	}
	replay := evidenceTestRoundTrip(t, struct {
		S *gateSubject
		C []reviewCheck
		A []reviewAnswer
		P reviewPolicy
	}{s, cs, as, p})
	if got := ReduceReview(replay.S, replay.C, replay.A, nil, replay.P); got.State != "pass" {
		t.Fatalf("lookup coverage lost on replay: %+v", got)
	}
	// There is still no evidence row for the empty lookup. That schema rule is
	// intentionally unchanged by allowing direct search coverage.
	_, _, _, _, control := reducerFixture("go")
	p.Packet.Evidence = control.Packet.Evidence
	if err := p.Packet.Validate(); err == nil || !strings.Contains(err.Error(), "no results") {
		t.Fatalf("fake zero-result evidence accepted: %v", err)
	}
}

func TestReduceAbsenceCoverageFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*reviewCheck, *reviewAnswer, *reviewPolicy)
	}{
		{"unknown ID", func(_ *reviewCheck, a *reviewAnswer, _ *reviewPolicy) { a.LookupIDs = []string{"missing"} }},
		{"duplicate ID", func(_ *reviewCheck, a *reviewAnswer, _ *reviewPolicy) {
			a.LookupIDs = append(a.LookupIDs, a.LookupIDs[0])
		}},
		{"stale lookup subject", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			p.Packet.Lookups[0].SubjectID = "different-subject"
		}},
		{"wrong scope", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) { p.Packet.Lookups[0].Scope = "other/" }},
		{"prefix sibling", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) { p.Packet.Lookups[0].Scope = "src2/" }},
		{"narrower scope", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) { p.Packet.Lookups[0].Scope = "src/nested/" }},
		{"undeclared scope", func(c *reviewCheck, _ *reviewAnswer, _ *reviewPolicy) { c.SearchScope = "" }},
		{"escaping scope", func(c *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			c.SearchScope = "../outside"
			p.Packet.Lookups[0].Scope = "../outside"
		}},
		{"review reply is not search", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) { p.Packet.Lookups[0].Unit = "review_reply" }},
		{"source read is not search", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) { p.Packet.Lookups[0].Unit = "source_read" }},
		{"omitted window", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			p.Packet.Lookups[0].Omissions = []string{"retention cap"}
		}},
		{"partial", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			l := &p.Packet.Lookups[0]
			l.Status = "partial"
			l.Completeness = evidencePartial
		}},
		{"lower bound", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			l := &p.Packet.Lookups[0]
			l.Status = "partial"
			l.Completeness = evidencePartial
			l.CountKind = evidenceCountLowerBound
		}},
		{"failed", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			l := &p.Packet.Lookups[0]
			l.ExecutionStatus = lookupFailed
			l.Status = "failed"
			l.Completeness = evidencePartial
			l.CountKind = evidenceCountUnknown
			l.Counts.Total = nil
			l.Counts.Succeeded = evidenceTestPtr(0)
			l.Counts.Failed = evidenceTestPtr(1)
			l.Errors = []string{"provider failed"}
		}},
		{"not requested", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			l := &p.Packet.Lookups[0]
			l.ExecutionStatus = lookupNotRequested
			l.Status = ""
			l.Completeness = evidenceUnknown
			l.CountKind = evidenceCountUnknown
			l.Counts = lookupCounts{}
		}},
		{"unavailable", func(_ *reviewCheck, _ *reviewAnswer, p *reviewPolicy) {
			l := &p.Packet.Lookups[0]
			l.ExecutionStatus = lookupUnavailable
			l.Status = ""
			l.Completeness = evidenceUnknown
			l.CountKind = evidenceCountUnknown
			l.Counts = lookupCounts{}
			l.Errors = []string{"server unavailable"}
		}},
		{"missing assessment", func(_ *reviewCheck, a *reviewAnswer, _ *reviewPolicy) { a.Support = nil }},
		{"unauthorized assessment", func(_ *reviewCheck, a *reviewAnswer, _ *reviewPolicy) { a.Support.Assessor = "author" }},
		{"missing lookup coverage", func(_ *reviewCheck, a *reviewAnswer, _ *reviewPolicy) { a.LookupIDs = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, as, p := absenceReview()
			tc.mutate(&cs[0], &as[0], &p)
			if err := p.Packet.Validate(); err != nil {
				t.Fatalf("invalid regression setup: %v", err)
			}
			r := ReduceReview(s, cs, as, nil, p)
			if r.State != "incomplete" || r.Completion.Completed != 0 {
				t.Fatalf("inadequate absence coverage passed: %+v", r)
			}
		})
	}
}

func TestReviewAnswerLookupIDsAreAdditive(t *testing.T) {
	_, _, as, _, _ := reducerFixture("go")
	b, err := json.Marshal(as[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "lookup_ids") {
		t.Fatalf("empty additive field changed old encoding: %s", b)
	}
	s, cs, newAnswers, p := absenceReview()
	newAnswers[0].Judgment = reviewJudgmentReuse
	if err := newAnswers[0].Validate(cs[0].Item, p.Packet, s, nil); err == nil {
		t.Fatal("lookup-only support authorized reuse without source evidence")
	}
}
