package main

// legacyGateOutputStatus is the temporary write-only default stdout adapter.
// It is NOT used in records, approvals, reduction, bench capture or v2 views.
// Removal requires the separately requested default-output decision.
func legacyGateOutputStatus(rec *gateRecord) string {
	switch rec.State {
	case "pass", "fail", "experiment":
		return rec.State
	case "incomplete":
		if rec.Reduction != nil {
			for _, reason := range rec.Reduction.Reasons {
				if reason.Reason == reviewAwaitingDecision {
					return "fail"
				}
			}
		}
	}
	return "unverified"
}
