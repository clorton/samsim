// Command gpsinspect is the GPS telemetry inspector CLI (INS-01..06). It
// talks exclusively to the aggregator's public HTTP/WebSocket APIs (AG-07,
// AG-08, AG-06) -- never SQLite directly -- to inspect vehicles, trips, and
// raw telemetry; safely perform manual trip closure; watch live semantic
// events; and independently validate reconstruction invariants.
//
// Usage:
//
//	gpsinspect --help
//	gpsinspect vehicles
//	gpsinspect vehicle <vehicle-id>
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
