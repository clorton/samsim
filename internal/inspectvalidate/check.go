package inspectvalidate

import "fmt"

// Status is one check's outcome.
type Status string

const (
	StatusPass Status = "PASS"
	StatusFail Status = "FAIL"
	StatusSkip Status = "SKIP"
	StatusWarn Status = "WARN"
)

// maxEvidence bounds the number of concrete evidence examples attached to
// one check result (INS-05: "Limit evidence examples to 20 entries per
// check, report total violation count, and make truncation explicit").
const maxEvidence = 20

// Check is one V01..V10 result.
type Check struct {
	CheckID        string   `json:"check_id"`
	Status         Status   `json:"status"`
	Message        string   `json:"message"`
	Evidence       []string `json:"evidence"`
	ViolationCount int      `json:"violation_count"`
	Truncated      bool     `json:"truncated"`
}

// checkBuilder accumulates violations for one check, capping stored
// evidence at maxEvidence while still counting the true total.
type checkBuilder struct {
	id       string
	evidence []string
	total    int
}

func newCheckBuilder(id string) *checkBuilder {
	return &checkBuilder{id: id}
}

func (b *checkBuilder) violate(format string, args ...any) {
	b.total++
	if len(b.evidence) < maxEvidence {
		b.evidence = append(b.evidence, fmt.Sprintf(format, args...))
	}
}

func (b *checkBuilder) result(passMessage string) Check {
	if b.total == 0 {
		return Check{CheckID: b.id, Status: StatusPass, Message: passMessage, Evidence: []string{}}
	}
	return Check{
		CheckID: b.id, Status: StatusFail,
		Message:        fmt.Sprintf("%d violation(s) found", b.total),
		Evidence:       b.evidence,
		ViolationCount: b.total,
		Truncated:      b.total > len(b.evidence),
	}
}

func skip(id, message string) Check {
	return Check{CheckID: id, Status: StatusSkip, Message: message, Evidence: []string{}}
}

func warn(id, message string) Check {
	return Check{CheckID: id, Status: StatusWarn, Message: message, Evidence: []string{}}
}

func pass(id, message string) Check {
	return Check{CheckID: id, Status: StatusPass, Message: message, Evidence: []string{}}
}
