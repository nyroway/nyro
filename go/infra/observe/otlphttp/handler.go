package otlphttp

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/infra/observe"
	"github.com/nyroway/nyro/go/infra/observe/internal/queue"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

const protobufContentType = "application/x-protobuf"

func (r *Receiver) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		r.writeError(writer, http.StatusMethodNotAllowed, codes.InvalidArgument, "OTLP endpoint requires POST")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, protobufContentType) {
		r.writeError(writer, http.StatusUnsupportedMediaType, codes.InvalidArgument, "OTLP endpoint requires application/x-protobuf")
		return
	}
	var export observe.ExportRequest
	var response proto.Message
	switch request.URL.Path {
	case "/v1/logs":
		export.Logs = &collectlogs.ExportLogsServiceRequest{}
		response = &collectlogs.ExportLogsServiceResponse{}
	case "/v1/metrics":
		export.Metrics = &collectmetrics.ExportMetricsServiceRequest{}
		response = &collectmetrics.ExportMetricsServiceResponse{}
	case "/v1/traces":
		export.Traces = &collecttrace.ExportTraceServiceRequest{}
		response = &collecttrace.ExportTraceServiceResponse{}
	default:
		r.writeError(writer, http.StatusNotFound, codes.NotFound, "unknown OTLP endpoint")
		return
	}
	payload, status, err := r.readBody(request)
	if err != nil {
		code := codes.InvalidArgument
		if status == http.StatusRequestEntityTooLarge {
			code = codes.ResourceExhausted
		}
		r.writeError(writer, status, code, err.Error())
		return
	}
	message := exportMessage(export)
	if err := proto.Unmarshal(payload, message); err != nil {
		r.writeError(writer, http.StatusBadRequest, codes.InvalidArgument, "invalid OTLP protobuf payload")
		return
	}
	export.ReceivedAt = time.Now()
	if err := r.queue.Push(queue.Item{Value: export, Bytes: len(payload)}); err != nil {
		r.rejected.Add(1)
		writer.Header().Set("Retry-After", "1")
		message := "OTLP receiver queue is unavailable"
		if errors.Is(err, queue.ErrFull) {
			message = "OTLP receiver queue is full"
		}
		r.writeError(writer, http.StatusServiceUnavailable, codes.Unavailable, message)
		return
	}
	r.acceptedBatches.Add(1)
	r.acceptedBytes.Add(uint64(len(payload)))
	r.writeMessage(writer, http.StatusOK, response)
}

func exportMessage(request observe.ExportRequest) proto.Message {
	if request.Logs != nil {
		return request.Logs
	}
	if request.Metrics != nil {
		return request.Metrics
	}
	return request.Traces
}

func (r *Receiver) readBody(request *http.Request) ([]byte, int, error) {
	if request.ContentLength > r.maxRequestBytes {
		return nil, http.StatusRequestEntityTooLarge, errors.New("OTLP request exceeds size limit")
	}
	encoded, err := io.ReadAll(io.LimitReader(request.Body, r.maxRequestBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("read OTLP request: %w", err)
	}
	if int64(len(encoded)) > r.maxRequestBytes {
		return nil, http.StatusRequestEntityTooLarge, errors.New("OTLP request exceeds size limit")
	}
	encoding := strings.TrimSpace(strings.ToLower(request.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		return encoded, http.StatusOK, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(encoded))
		if err != nil {
			return nil, http.StatusBadRequest, errors.New("invalid gzip OTLP request")
		}
		decompressed, readErr := io.ReadAll(io.LimitReader(reader, r.maxRequestBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			return nil, http.StatusBadRequest, errors.New("invalid gzip OTLP request")
		}
		if int64(len(decompressed)) > r.maxRequestBytes {
			return nil, http.StatusRequestEntityTooLarge, errors.New("OTLP request exceeds size limit after decompression")
		}
		return decompressed, http.StatusOK, nil
	default:
		return nil, http.StatusUnsupportedMediaType, errors.New("unsupported OTLP content encoding")
	}
}

func (r *Receiver) writeError(writer http.ResponseWriter, httpStatus int, code codes.Code, message string) {
	r.writeMessage(writer, httpStatus, &statuspb.Status{Code: int32(code), Message: message})
}

func (r *Receiver) writeMessage(writer http.ResponseWriter, status int, message proto.Message) {
	payload, err := proto.Marshal(message)
	if err != nil {
		http.Error(writer, "encode protobuf response", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", protobufContentType)
	writer.WriteHeader(status)
	_, _ = writer.Write(payload)
}
