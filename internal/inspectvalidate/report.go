package inspectvalidate

// OverallStatus summarizes a Report's aggregate outcome.
type OverallStatus string

const (
	OverallPass                OverallStatus = "PASS"
	OverallPassWithLimitations OverallStatus = "PASS_WITH_LIMITATIONS"
	OverallFail                OverallStatus = "FAIL"
)

// Report is the full `validate` command report.
type Report struct {
	DatabaseID string  `json:"database_id"`
	VehicleID  string  `json:"vehicle_id"`
	Revision   int64   `json:"revision"`
	Checks     []Check `json:"checks"`

	PassCount int `json:"pass_count"`
	FailCount int `json:"fail_count"`
	SkipCount int `json:"skip_count"`
	WarnCount int `json:"warn_count"`

	Overall OverallStatus `json:"overall"`
	Scope   []string      `json:"scope_and_limitations"`
}

// scopeNotes documents validate's known limitations, always included so a
// consumer never mistakes a passing report for more than it is (INS-05:
// "Snapshot validation cannot prove historical packet idempotency, atomic
// crash behavior or the absence of past transient errors; those require
// aggregator integration tests").
var scopeNotes = []string{
	"This validation reads current aggregator state through public APIs only; it does not open SQLite or re-run the aggregator's own reconstruction code.",
	"A passing report does not prove historical packet idempotency, atomic crash behavior, or the absence of past transient errors; those require aggregator integration tests.",
	"No detected internal completeness gaps (V08) does not prove all boundary telemetry has arrived; boundary completeness is always UNKNOWN.",
	"V09/V10 (immutability/monotonicity across runs) require an explicit --baseline from a prior run; without one they are SKIP.",
}

// BuildReport assembles the full V01..V10 check suite into a Report.
func BuildReport(obs *Observation, baseline *Baseline) *Report {
	checks := []Check{
		CheckRawIdentity(obs),
		CheckCurrentMembership(obs),
		CheckTripMetadata(obs),
		CheckTemporalComponents(obs),
		CheckManualBarriers(obs),
		CheckActivity(obs),
		CheckLineage(obs),
		CheckCompleteness(obs),
		CheckObservedImmutability(obs, baseline),
		CheckObservedMonotonicity(obs, baseline),
	}

	r := &Report{
		DatabaseID: obs.DatabaseID, VehicleID: obs.VehicleID, Revision: obs.Revision,
		Checks: checks, Scope: scopeNotes,
	}
	anySkippedTemporal := false
	for _, c := range checks {
		switch c.Status {
		case StatusPass:
			r.PassCount++
		case StatusFail:
			r.FailCount++
		case StatusSkip:
			r.SkipCount++
			if c.CheckID == "V09" || c.CheckID == "V10" {
				anySkippedTemporal = true
			}
		case StatusWarn:
			r.WarnCount++
		}
	}

	switch {
	case r.FailCount > 0:
		r.Overall = OverallFail
	case anySkippedTemporal:
		r.Overall = OverallPassWithLimitations
	default:
		r.Overall = OverallPass
	}
	return r
}

// HasFail reports whether any check in the report is a FAIL.
func (r *Report) HasFail() bool {
	return r.FailCount > 0
}
