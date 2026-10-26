package simulator

import (
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// Browser uploads are validated with the same parser as configured route files.
// Bytes stay in this simulator process and disappear on restart. Filenames are
// never used as paths. A READY/empty precondition prevents changing active runs.
func (s *Server) handleUploadRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "text/csv" {
		writeError(w, r, NewAPIError(CodeUnsupportedMediaType, "upload requires text/csv"))
		return
	}
	e := s.Engine
	e.mu.Lock()
	limit, rowsLimit := e.cfg.MaxRouteBytes, e.cfg.MaxRouteRows
	e.mu.Unlock()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		writeError(w, r, NewAPIError(CodeRouteTooLarge, "CSV exceeds upload limit"))
		return
	}
	records, err := LoadRouteCSV(data, LoadRouteCSVOptions{ResolvedRunStartMs: 1, MaxBytes: limit, MaxRows: rowsLimit})
	if err != nil {
		writeError(w, r, NewAPIError(CodeInvalidRoute, err.Error()))
		return
	}
	sum := sha256.Sum256(data)
	id := fmt.Sprintf("upload-%x", sum[:12])
	e.controlMu.Lock()
	defer e.controlMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.playbackState != PlaybackReady || e.totalUnsentLocked() != 0 {
		writeError(w, r, NewAPIError(CodeInvalidConfig, "CSV upload requires READY with no unsent fixes; stop and reset first"))
		return
	}
	if e.uploadedRoutes == nil {
		e.uploadedRoutes = make(map[string][]byte)
	}
	// Bound retained upload memory, independently of per-file limits.
	var retained int64
	for _, b := range e.uploadedRoutes {
		retained += int64(len(b))
	}
	if _, exists := e.uploadedRoutes[id]; !exists && (retained+int64(len(data)) > 2*limit || len(e.uploadedRoutes) >= 16) {
		writeError(w, r, NewAPIError(CodeRouteTooLarge, "upload cache full; restart simulator to clear uploaded routes"))
		return
	}
	e.uploadedRoutes[id] = data
	routes := make(map[string]string, len(e.cfg.Routes)+1)
	for k, v := range e.cfg.Routes {
		routes[k] = v
	}
	routes[id] = "uploaded-in-memory"
	e.cfg.Routes = routes
	e.controlRevision++
	writeJSON(w, http.StatusOK, map[string]any{"route_id": id, "fix_count": len(records), "duration_ms": records[len(records)-1].TimestampMs - 1, "first_offset_ms": records[0].TimestampMs - 1, "control_revision": e.controlRevision, "persistent": false})
}
