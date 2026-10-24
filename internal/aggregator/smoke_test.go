package aggregator

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

// TestSmoke_IngestThroughput is AG-10's "modest throughput smoke run": it
// submits packets of 10 fixes at a target rate for a bounded duration and
// reports actual observed throughput, latency percentiles, and error count.
// It is explicitly NOT a pass/fail performance assertion (the spec warns
// against promising a machine-independent target); it only fails if any
// ingestion call returns an unexpected error, since durability/correctness
// must never be sacrificed to hit a throughput number.
//
// Skipped under `go test -short`. Defaults to a modest 3s/1000 fixes-per-
// second run so the normal `go test ./...` suite stays fast; override with
// the SMOKE_DURATION_SECONDS and SMOKE_FIXES_PER_SEC environment variables
// for a heavier run, e.g.:
//
//	SMOKE_DURATION_SECONDS=30 SMOKE_FIXES_PER_SEC=1000 go test ./internal/aggregator/ -run TestSmoke -v
func TestSmoke_IngestThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping throughput smoke test in -short mode")
	}

	const batchSize = 10
	duration := envDurationSeconds("SMOKE_DURATION_SECONDS", 3)
	targetFixesPerSec := envInt("SMOKE_FIXES_PER_SEC", 1000)
	targetPacketsPerSec := targetFixesPerSec / batchSize
	if targetPacketsPerSec < 1 {
		targetPacketsPerSec = 1
	}

	svc, _ := newTestService(t)

	packetInterval := time.Second / time.Duration(targetPacketsPerSec)
	deadline := time.Now().Add(duration)

	var latencies []time.Duration
	var errCount int
	var packetCount int
	var fixCount int
	startedAt := time.Now()

	seq := 1
	tsOffset := int64(0)
	for time.Now().Before(deadline) {
		iterStart := time.Now()

		pkt := &telemetryv1.TelemetryPacket{
			SimulatorId: "sim-smoke",
			VehicleId:   "vehicle-smoke",
			PacketId:    fmt.Sprintf("20261024-%06d", seq),
		}
		for i := 0; i < batchSize; i++ {
			pkt.Fixes = append(pkt.Fixes, &telemetryv1.GPSFix{
				TimestampMs:    baseMs + tsOffset,
				Latitude:       1,
				Longitude:      1,
				SpeedMps:       1,
				HeadingDegrees: 0,
			})
			tsOffset++
		}
		seq++

		callStart := time.Now()
		_, err := ingestNoFatal(svc, pkt)
		latency := time.Since(callStart)
		latencies = append(latencies, latency)

		if err != nil {
			errCount++
			t.Logf("smoke: ingestion error at packet %d: %v", seq, err)
		} else {
			packetCount++
			fixCount += batchSize
		}

		if sleep := packetInterval - time.Since(iterStart); sleep > 0 {
			time.Sleep(sleep)
		}
	}

	elapsed := time.Since(startedAt)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := percentile(latencies, 0.50)
	p99 := percentile(latencies, 0.99)
	maxLatency := time.Duration(0)
	if len(latencies) > 0 {
		maxLatency = latencies[len(latencies)-1]
	}
	actualFixesPerSec := float64(fixCount) / elapsed.Seconds()
	actualPacketsPerSec := float64(packetCount) / elapsed.Seconds()

	t.Logf("smoke: elapsed=%s target=%d fixes/s (%d packets/s) actual=%.1f fixes/s (%.1f packets/s) packets=%d fixes=%d errors=%d p50=%s p99=%s max=%s",
		elapsed, targetFixesPerSec, targetPacketsPerSec, actualFixesPerSec, actualPacketsPerSec, packetCount, fixCount, errCount, p50, p99, maxLatency)

	if errCount > 0 {
		t.Fatalf("smoke: %d/%d ingestion calls failed; durability/correctness issue, not just a throughput shortfall", errCount, packetCount+errCount)
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func envDurationSeconds(key string, defaultSeconds int) time.Duration {
	return time.Duration(envInt(key, defaultSeconds)) * time.Second
}

func envInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}
