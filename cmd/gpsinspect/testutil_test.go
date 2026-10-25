package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// runCLI invokes the CLI in-process (no os.Exit) against args, feeding
// stdin, and returns stdout, stderr and the exit code. This is the
// INS-06-mandated harness: "Tests must not require an interactive terminal
// or a running service unless labeled integration."
func runCLI(t *testing.T, stdin string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	var in io.Reader = strings.NewReader(stdin)
	exitCode = run(args, in, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String(), exitCode
}
