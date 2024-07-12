package libhoneyreceiver

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	semconv "go.opentelemetry.io/collector/semconv/v1.16.0"
	trc "go.opentelemetry.io/otel/trace"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/status"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/errors"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/eventtime"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/httphelper"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/trace"
)

// Pre-computed status with code=Internal to be used in case of a marshaling error.
var fallbackMsg = []byte(`{"code": 13, "message": "failed to marshal error message"}`)

const fallbackContentType = "application/json"

func handleTraces(resp http.ResponseWriter, req *http.Request, tracesReceiver *trace.Receiver, cfg Config) {
	enc, ok := readContentType(resp, req)
	if !ok {
		return
	}

	// simpleSpans, err := readInputPorgressively(resp, req, enc) // enc.unmarshalTracesRequest(body)
	// if err != nil {
	// 	writeError(resp, enc, err, http.StatusBadRequest)
	// 	return
	// }

	body, err := io.ReadAll(req.Body)
	if err != nil {
		writeError(resp, enc, err, http.StatusBadRequest)
	}
	if err = req.Body.Close(); err != nil {
		writeError(resp, enc, err, http.StatusBadRequest)
	}

	dataset, _ := getDatasetFromRequest(req.URL.RawPath)
	// if there's an error, maybe we should check inside the spans for a service.name?
	for _, p := range cfg.HTTP.TracesURLPaths {
		dataset = strings.Replace(dataset, p, "", 1)
	}

	simpleSpans := make([]simpleSpan, 0)
	err = json.Unmarshal(body, &simpleSpans)
	if err != nil {
		writeError(resp, enc, err, http.StatusBadRequest)
	}

	otlpTraces, err := toTraces(dataset, simpleSpans, cfg)
	if err != nil {
		writeError(resp, enc, err, http.StatusBadRequest)
		return
	}

	otlpReq := ptraceotlp.NewExportRequestFromTraces(otlpTraces)

	otlpResp, err := tracesReceiver.Export(req.Context(), otlpReq)
	if err != nil {
		writeError(resp, enc, err, http.StatusInternalServerError)
		return
	}

	msg, err := enc.marshalTracesResponse(otlpResp)
	if err != nil {
		writeError(resp, enc, err, http.StatusInternalServerError)
		return
	}
	writeResponse(resp, enc.contentType(), http.StatusOK, msg)
}

func readInputProgressively(resp http.ResponseWriter, req *http.Request, enc encoder) ([]simpleSpan, error) {
	simpleSpans := make([]simpleSpan, 0)
	lineNum := 0
	defer func() {
		panic := recover()
		err, ok := panic.(error)
		// recover from panic if one occurred. Set err to nil otherwise.
		if !ok && err != nil {
			writeError(resp, enc, err, http.StatusBadRequest)
			return
		}
	}()

	scanner := bufio.NewScanner(req.Body)

	for scanner.Scan() {
		lineNum += 1
		ss := simpleSpan{Time: eventtime.GetEventTimeDefaultString(), Samplerate: 1} // defaults

		thisLine := scanner.Bytes()
		err := json.Unmarshal(thisLine, &ss)
		if err != nil {
			writeError(resp, enc, err, http.StatusBadRequest)
			fmt.Fprintf(os.Stderr, "encountered json error on line %d:\nError: %v\nJSON line: %v\n", lineNum, err.Error(), string(thisLine))
			continue
		}
		simpleSpans = append(simpleSpans, ss)
	}
	return simpleSpans, nil
}

// writeError encodes the HTTP error inside a rpc.Status message as required by the OTLP protocol.
func writeError(w http.ResponseWriter, encoder encoder, err error, statusCode int) {
	s, ok := status.FromError(err)
	if ok {
		statusCode = errors.GetHTTPStatusCodeFromStatus(s)
	} else {
		s = httphelper.NewStatusFromMsgAndHTTPCode(err.Error(), statusCode)
	}
	writeStatusResponse(w, encoder, statusCode, s.Proto())
}

// errorHandler encodes the HTTP error message inside a rpc.Status message as required
// by the OTLP protocol.
func errorHandler(w http.ResponseWriter, r *http.Request, errMsg string, statusCode int) {
	s := httphelper.NewStatusFromMsgAndHTTPCode(errMsg, statusCode)
	switch getMimeTypeFromContentType(r.Header.Get("Content-Type")) {
	case jsonContentType:
		writeStatusResponse(w, jsEncoder, statusCode, s.Proto())
		return
	}
	writeResponse(w, fallbackContentType, http.StatusInternalServerError, fallbackMsg)
}

func writeStatusResponse(w http.ResponseWriter, enc encoder, statusCode int, rsp *spb.Status) {
	msg, err := enc.marshalStatus(rsp)
	if err != nil {
		writeResponse(w, fallbackContentType, http.StatusInternalServerError, fallbackMsg)
		return
	}

	writeResponse(w, enc.contentType(), statusCode, msg)
}

func readContentType(resp http.ResponseWriter, req *http.Request) (encoder, bool) {
	if req.Method != http.MethodPost {
		handleUnmatchedMethod(resp)
		return nil, false
	}

	switch getMimeTypeFromContentType(req.Header.Get("Content-Type")) {
	case jsonContentType:
		return jsEncoder, true
	default:
		handleUnmatchedContentType(resp)
		return nil, false
	}
}

func writeResponse(w http.ResponseWriter, contentType string, statusCode int, msg []byte) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(statusCode)
	// Nothing we can do with the error if we cannot write to the response.
	_, _ = w.Write(msg)
}

func getMimeTypeFromContentType(contentType string) string {
	mediatype, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return mediatype
}

func handleUnmatchedMethod(resp http.ResponseWriter) {
	status := http.StatusMethodNotAllowed
	writeResponse(resp, "text/plain", status, []byte(fmt.Sprintf("%v method not allowed, supported: [POST]", status)))
}

func handleUnmatchedContentType(resp http.ResponseWriter) {
	status := http.StatusUnsupportedMediaType
	writeResponse(resp, "text/plain", status, []byte(fmt.Sprintf("%v unsupported media type, supported: [%s, %s]", status, jsonContentType, pbContentType)))
}

func SpanIDFrom(s string) trc.SpanID {
	hash := fnv.New64a()
	hash.Write([]byte(s))
	n := hash.Sum64()
	sid := trc.SpanID{}
	binary.LittleEndian.PutUint64(sid[:], n)
	return sid
}

func TraceIDFrom(s string) trc.TraceID {
	hash := fnv.New64a()
	hash.Write([]byte(s))
	n1 := hash.Sum64()
	hash.Write([]byte(s))
	n2 := hash.Sum64()
	tid := trc.TraceID{}
	binary.LittleEndian.PutUint64(tid[:], n1)
	binary.LittleEndian.PutUint64(tid[8:], n2)
	return tid
}

func fakeMeAnId(length int) []byte {
	token := make([]byte, length)
	rand.Read(token)
	return token
}

// taken from refinery https://github.com/honeycombio/refinery/blob/v2.6.1/route/route.go#L964-L974
func getDatasetFromRequest(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("missing dataset name")
	}
	dataset, err := url.PathUnescape(path)
	if err != nil {
		return "", err
	}
	return dataset, nil
}

type simpleSpan struct {
	Samplerate int                    `json:"samplerate"`
	Time       string                 `json:"time"` // epoch miliseconds.nanoseconds
	Data       map[string]interface{} `json:"data"`
}

// this should override unmarshall of spans to provide defaults
func (s *simpleSpan) UnmarshalJSON(j []byte) error {
	type _simpleSpan simpleSpan
	tmp := _simpleSpan{Time: eventtime.GetEventTimeDefaultString(), Samplerate: 1}

	err := json.Unmarshal(j, &tmp)
	if err != nil {
		return err
	}

	*s = simpleSpan(tmp)
	return nil
}

func toTraces(dataset string, ss []simpleSpan, cfg Config) (ptrace.Traces, error) {
	// Creating a map of service spans to slices
	// since the expectation is that `service.name`
	// is added as a resource attribute in most systems
	// now instead of being a span level attribute.
	slice := ptrace.NewSpanSlice()
	count := 0
	// foundServiceNames := []string{dataset}
	foundLibraryName := "libhoney_receiver"
	foundLibraryVersion := "1.0.0"

	for _, span := range ss {
		count += 1
		newSpan := slice.AppendEmpty()
		time_ns := eventtime.GetEventTimeNano(span.Time)
		// fmt.Fprintf(os.Stderr, "Old time (s): %d \nOffset (s): %d\nNew time (ns): %d\n", uint64(time_s*1000000000), secondsOffset, time_ns)

		duration_ms := 0.0
		for _, df := range cfg.Attributes.DurationFields {
			if duration, okay := span.Data[df]; okay {
				duration_ms = duration.(float64)
				break
			}
		}
		end_timestamp := time_ns + (int64(duration_ms) * 1000000)

		if tid, ok := span.Data[cfg.Attributes.TraceId]; ok {
			tid := strings.Replace(tid.(string), "-", "", -1)
			if len(tid) > 32 {
				// fmt.Println("SpanID was too long now it's shortened to: %v", sid)
				tid = tid[0:32]
			}
			newTraceId := pcommon.TraceID(TraceIDFrom(tid))
			newSpan.SetTraceID(newTraceId)
		} else {
			newSpan.SetTraceID(pcommon.TraceID(fakeMeAnId(32)))
		}

		if sid, ok := span.Data[cfg.Attributes.SpanId]; ok {
			sid := strings.Replace(sid.(string), "-", "", -1)
			if len(sid) == 32 {
				// take the middle
				sid = sid[8:24]
			} else if len(sid) > 16 {
				// fmt.Println("SpanID was too long now it's shortened to: %v", sid)
				sid = sid[0:16]
			}
			newTraceId := pcommon.SpanID(SpanIDFrom(sid))
			newSpan.SetSpanID(newTraceId)
		} else {
			newSpan.SetSpanID(pcommon.SpanID(fakeMeAnId(16)))
		}

		newSpan.SetStartTimestamp(pcommon.Timestamp(time_ns))
		newSpan.SetEndTimestamp(pcommon.Timestamp(end_timestamp))
		if pid, ok := span.Data[cfg.Attributes.ParentId]; ok {
			newSpan.SetParentSpanID(pcommon.SpanID(SpanIDFrom(pid.(string))))
		}

		if serviceName, ok := span.Data[cfg.Resources.ServiceName]; ok {
			// this is to see if refinery emits 1 batch per dataset or if it relies on the service.name field
			if serviceName.(string) != dataset {
				newSpan.Attributes().PutStr("libhoney.receiver.service_name", serviceName.(string))
			}
		}
		if libraryName, ok := span.Data[cfg.Scopes.LibraryName]; ok {
			if libraryName != foundLibraryName {
				newSpan.Attributes().PutStr("libhoney.receiver.library_name", libraryName.(string))
			}
			foundLibraryName = libraryName.(string)
		}
		if libraryVersion, ok := span.Data[cfg.Scopes.LibraryVersion]; ok {
			if libraryVersion != foundLibraryName {
				newSpan.Attributes().PutStr("libhoney.receiver.library_vesion", libraryVersion.(string))
			}
			foundLibraryVersion = libraryVersion.(string)
		}

		newSpan.SetName(span.Data[cfg.Attributes.Name].(string))
		newSpan.Status().SetCode(ptrace.StatusCodeOk)

		if _, ok := span.Data[cfg.Attributes.Error]; ok {
			newSpan.Status().SetCode(ptrace.StatusCodeError)
		}

		switch span.Data[cfg.Attributes.SpanKind].(string) {
		case "server":
			newSpan.SetKind(ptrace.SpanKindServer)
		case "client":
			newSpan.SetKind(ptrace.SpanKindClient)
		case "producer":
			newSpan.SetKind(ptrace.SpanKindProducer)
		case "consumer":
			newSpan.SetKind(ptrace.SpanKindConsumer)
		case "internal":
			newSpan.SetKind(ptrace.SpanKindInternal)
		default:
			newSpan.SetKind(ptrace.SpanKindUnspecified)
		}

		newSpan.Attributes().PutInt("SampleRate", int64(span.Samplerate))

		already_used_fields := []string{cfg.Resources.ServiceName, cfg.Scopes.LibraryName, cfg.Scopes.LibraryVersion}
		already_used_fields = append(already_used_fields, cfg.Attributes.Name,
			cfg.Attributes.TraceId, cfg.Attributes.ParentId, cfg.Attributes.SpanId,
			cfg.Attributes.Error, cfg.Attributes.SpanKind,
		)
		already_used_fields = append(already_used_fields, cfg.Attributes.DurationFields...)

		for k, v := range span.Data {
			if slices.Contains(already_used_fields, k) {
				continue
			}
			switch v := v.(type) {
			case string:
				newSpan.Attributes().PutStr(k, v)
			case int:
				newSpan.Attributes().PutInt(k, int64(v))
			case float64:
				newSpan.Attributes().PutDouble(k, v)
			case bool:
				newSpan.Attributes().PutBool(k, v)
			default:
				fmt.Fprintf(os.Stderr, "data type issue: %v is the key for type %t where value is %v", k, v, v)
			}
		}
	}

	results := ptrace.NewTraces()
	rs := results.ResourceSpans().AppendEmpty()
	rs.SetSchemaUrl(semconv.SchemaURL)
	// sharedAttributes.CopyTo(rs.Resource().Attributes())
	rs.Resource().Attributes().PutStr(semconv.AttributeServiceName, dataset)

	in := rs.ScopeSpans().AppendEmpty()
	in.Scope().SetName(foundLibraryName)
	in.Scope().SetVersion(foundLibraryVersion)
	slice.CopyTo(in.Spans())

	return results, nil
}
