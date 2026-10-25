package inspectoutput

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// FormatTimestampMs renders an epoch-millisecond event timestamp as UTC
// ISO-8601 with milliseconds, per INS spec section 3 ("UTC ISO-8601
// timestamps with milliseconds and raw epoch ms in detailed reports").
func FormatTimestampMs(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	return t.Format("2006-01-02T15:04:05.000Z")
}

// FormatTimestampMsPtr renders a nullable timestamp, using "-" for nil.
func FormatTimestampMsPtr(ms *int64) string {
	if ms == nil {
		return "-"
	}
	return FormatTimestampMs(*ms)
}

// StringOrDash renders a nullable string pointer, using "-" for nil.
func StringOrDash(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// Table writes a simple column-aligned table for list output.
type Table struct {
	w       *tabwriter.Writer
	headers []string
}

// NewTable starts a new table with the given column headers, writing to w.
func NewTable(w io.Writer, headers ...string) *Table {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	return &Table{w: tw, headers: headers}
}

// Row appends one row of values.
func (t *Table) Row(values ...string) {
	fmt.Fprintln(t.w, strings.Join(values, "\t"))
}

// Flush writes the buffered table to the underlying writer.
func (t *Table) Flush() error {
	return t.w.Flush()
}

// Fields writes a sequence of "Label: value" lines for detail reports.
type Fields struct {
	w io.Writer
}

func NewFields(w io.Writer) *Fields { return &Fields{w: w} }

func (f *Fields) Line(label, value string) {
	fmt.Fprintf(f.w, "%s: %s\n", label, value)
}

func (f *Fields) Blank() {
	fmt.Fprintln(f.w)
}
