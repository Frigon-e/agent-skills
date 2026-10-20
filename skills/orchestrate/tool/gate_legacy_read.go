package main

import (
	"encoding/json"
	"strings"
)

// historicalGateResult is a read-only compatibility boundary for stored gate
// records, bench archives and report prose. A historical label is NOT a v2
// state: in particular, "unverified" cannot distinguish error/stale/incomplete.
// New runs never populate it. Re-exports use an explicit historical label, never
// resurrect the removed status field or manufacture a historical review state.
type historicalGateResult struct {
	HistoricalStatus string `json:"historical_status,omitempty"`
}

func (r *gateRecord) UnmarshalJSON(data []byte) error {
	type current gateRecord
	var decoded struct {
		*current
		Status string `json:"status"`
	}
	*r = gateRecord{}
	decoded.current = (*current)(r)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if r.State == "" && r.HistoricalStatus == "" {
		r.HistoricalStatus = decoded.Status
	}
	return nil
}

func (r *benchRun) UnmarshalJSON(data []byte) error {
	type current benchRun
	var decoded struct {
		*current
		Status string `json:"status"`
	}
	// The archive loader assigns this private path before decoding.
	dir := r.dir
	*r = benchRun{dir: dir}
	decoded.current = (*current)(r)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if r.State == "" && r.HistoricalStatus == "" {
		r.HistoricalStatus = decoded.Status
	}
	return nil
}

// storedGateResultLabel is for history display and import completeness only.
// Approval always requires a recorded v2 state and a fresh reducer replay.
func storedGateResultLabel(r *gateRecord) string {
	if r.State != "" {
		return r.State
	}
	return r.HistoricalStatus
}

func retainHistoricalGateResult(run *benchRun, rec *gateRecord) {
	if rec.State == "" {
		run.historicalGateResult = rec.historicalGateResult
	}
}

func readStoredGateReport(report string) (string, historicalGateResult) {
	var historical historicalGateResult
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "Review state: **") {
			state, _, ok := strings.Cut(strings.TrimPrefix(line, "Review state: **"), "**")
			if ok {
				return state, historicalGateResult{}
			}
		}
		if strings.HasPrefix(line, "Status:") && historical.HistoricalStatus == "" {
			// Keep the exact old prose value, including any formatting/version.
			historical.HistoricalStatus = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
	}
	return "", historical
}
