package telemetry

import (
	"context"
	"errors"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// maxBatchBytes keeps a request under Langfuse's limit of about 4.5 MB,
// which counts the body uncompressed. A model call's span holds the whole
// conversation, so a few of them fill a request.
const maxBatchBytes = 3 << 20

// batches sends spans in requests of at most limit bytes, by their
// attributes; a span over the limit goes alone.
type batches struct {
	sdktrace.SpanExporter
	limit int
}

func (b batches) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	var errs []error
	start, size := 0, 0
	for i, s := range spans {
		n := spanBytes(s)
		if i > start && size+n > b.limit {
			errs = append(errs, b.SpanExporter.ExportSpans(ctx, spans[start:i]))
			start, size = i, 0
		}
		size += n
	}
	if start < len(spans) {
		errs = append(errs, b.SpanExporter.ExportSpans(ctx, spans[start:]))
	}
	return errors.Join(errs...)
}

// spanBytes estimates the size of s in a request.
func spanBytes(s sdktrace.ReadOnlySpan) int {
	n := 256 + len(s.Name()) // IDs, times, kind and status
	for _, kv := range s.Attributes() {
		n += len(kv.Key) + len(kv.Value.Emit())
	}
	for _, e := range s.Events() {
		n += len(e.Name)
		for _, kv := range e.Attributes {
			n += len(kv.Key) + len(kv.Value.Emit())
		}
	}
	return n
}
