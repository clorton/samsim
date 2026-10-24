package httpapi

import (
	"io"
	"mime"
	"net/http"

	"gpsdemo/internal/apierr"
	"gpsdemo/internal/wire"

	telemetryv1 "gpsdemo/gen/telemetry/v1"
)

// handleIngest implements POST /api/v1/telemetry (AG-02/AG-04): decodes
// JSON or binary Protobuf by Content-Type, validates, normalizes, and runs
// the full ingestion pipeline.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}

	contentType := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		writeError(w, r, apierr.Newf(apierr.CodeInvalidRequest, "malformed Content-Type header: %v", err))
		return
	}
	if mediaType == wire.ContentTypeJSON {
		if cs, ok := params["charset"]; ok && cs != "" && !isUTF8Charset(cs) {
			writeError(w, r, apierr.Newf(apierr.CodeUnsupportedMediaType, "unsupported charset %q", cs))
			return
		}
	} else if mediaType != wire.ContentTypeProtobuf {
		writeError(w, r, apierr.Newf(apierr.CodeUnsupportedMediaType, "unsupported media type %q", mediaType))
		return
	}

	if ce := r.Header.Get("Content-Encoding"); ce != "" {
		writeError(w, r, apierr.Newf(apierr.CodeUnsupportedContentEnc, "unsupported Content-Encoding %q", ce))
		return
	}

	limitedBody := http.MaxBytesReader(w, r.Body, s.MaxBodyBytes)
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		writeError(w, r, apierr.Newf(apierr.CodeBodyTooLarge, "request body exceeds max_body_bytes=%d", s.MaxBodyBytes))
		return
	}

	var pkt *telemetryv1.TelemetryPacket
	if mediaType == wire.ContentTypeJSON {
		pkt, err = wire.DecodeJSONPacket(body)
	} else {
		pkt, err = wire.DecodeProtobufPacket(body)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}

	if err := wire.ValidatePacketSemantics(pkt, s.MaxFixesPerPacket); err != nil {
		writeError(w, r, err)
		return
	}

	np, err := wire.NormalizePacket(pkt)
	if err != nil {
		writeError(w, r, err)
		return
	}

	result, err := s.Service.Ingest(r.Context(), np)
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func isUTF8Charset(cs string) bool {
	switch cs {
	case "utf-8", "UTF-8", "Utf-8":
		return true
	default:
		return false
	}
}
