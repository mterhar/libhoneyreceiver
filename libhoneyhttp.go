package libhoneyreceiver

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/ptrace"
	semconv "go.opentelemetry.io/collector/semconv/v1.16.0"
	trc "go.opentelemetry.io/otel/trace"

	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/status"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/errors"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/eventtime"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/httphelper"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/logs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/trace"
	"github.com/vmihailenco/msgpack/v5"
)

// Pre-computed status with code=Internal to be used in case of a marshaling error.
var fallbackMsg = []byte(`{"code": 13, "message": "failed to marshal error message"}`)

const fallbackContentType = "application/json"

func handleSomething(resp http.ResponseWriter, req *http.Request, tracesReceiver *trace.Receiver, logsReceiver *logs.Receiver, cfg Config, logger zap.Logger) {
	// fmt.Println("Got a thing!")
	enc, ok := readContentType(resp, req)
	if !ok {
		return
	}

	dataset, err := getDatasetFromRequest(req.RequestURI)
	if err != nil {
		logger.Info("No dataset found in URL", zap.String("req.RequstURI", req.RequestURI))
	}

	for _, p := range cfg.HTTP.TracesURLPaths {
		dataset = strings.Replace(dataset, p, "", 1)
		logger.Debug("dataset parsed", zap.String("dataset.parsed", dataset))
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		writeError(resp, enc, err, http.StatusBadRequest)
	}
	if err = req.Body.Close(); err != nil {
		writeError(resp, enc, err, http.StatusBadRequest)
	}

	simpleSpans := make([]simpleSpan, 0)
	switch req.Header.Get("Content-Type") {
	case "application/x-msgpack", "application/msgpack":
		decoder := msgpack.NewDecoder(bytes.NewReader(body))
		decoder.UseLooseInterfaceDecoding(true)
		decoder.Decode(&simpleSpans)
		if len(simpleSpans) > 0 {
			logger.Debug("Decoding with msgpack worked", zap.Time("timestamp.first.msgpacktimestamp", *simpleSpans[0].MsgPackTimestamp), zap.String("timestamp.first.time", simpleSpans[0].Time))
			logger.Debug("span zero", zap.String("span.data", simpleSpans[0].DebugString()))
		}
	case jsonContentType:
		err = json.Unmarshal(body, &simpleSpans)
		if err != nil {
			writeError(resp, enc, err, http.StatusBadRequest)
		}
		if len(simpleSpans) > 0 {
			logger.Debug("Decoding with json worked", zap.Time("timestamp.first.msgpacktimestamp", *simpleSpans[0].MsgPackTimestamp), zap.String("timestamp.first.time", simpleSpans[0].Time))
		}
	}

	otlpTraces, otlpLogs, err := toPsomething(dataset, simpleSpans, cfg, logger)
	if err != nil {
		writeError(resp, enc, err, http.StatusBadRequest)
		return
	}

	otlpReqTrace := ptraceotlp.NewExportRequestFromTraces(otlpTraces)

	otlpResp, err := tracesReceiver.Export(req.Context(), otlpReqTrace)
	if err != nil {
		writeError(resp, enc, err, http.StatusInternalServerError)
		return
	}

	msg, err := enc.marshalTracesResponse(otlpResp)
	if err != nil {
		writeError(resp, enc, err, http.StatusInternalServerError)
		return
	}

	otlpReqLog := plogotlp.NewExportRequestFromLogs(otlpLogs)

	otlpLogResp, err := logsReceiver.Export(req.Context(), otlpReqLog)
	if err != nil {
		writeError(resp, enc, err, http.StatusInternalServerError)
		return
	}

	_, err = enc.marshalLogsResponse(otlpLogResp)
	if err != nil {
		writeError(resp, enc, err, http.StatusInternalServerError)
		return
	}

	writeResponse(resp, enc.contentType(), http.StatusOK, msg)
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
	case "application/x-msgpack", "application/msgpack":
		return mpEncoder, true
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
	Samplerate       int                    `json:"samplerate" msgpack:"samplerate"`
	MsgPackTimestamp *time.Time             `msgpack:"time"`
	Time             string                 `json:"time"` // should not be trusted. use MsgPackTimestamp
	Data             map[string]interface{} `json:"data" msgpack:"data"`
}

// Overrides unmarshall to make sure the MsgPackTimestamp is set
func (s *simpleSpan) UnmarshalJSON(j []byte) error {
	type _simpleSpan simpleSpan
	tstr := eventtime.GetEventTimeDefaultString()
	tzero := time.Time{}
	tmp := _simpleSpan{Time: "none", MsgPackTimestamp: &tzero, Samplerate: 1}

	err := json.Unmarshal(j, &tmp)
	if err != nil {
		return err
	}
	if tmp.MsgPackTimestamp.IsZero() && tmp.Time == "none" {
		// neither timestamp was set. give it right now.
		tmp.Time = tstr
		tnow := time.Now()
		tmp.MsgPackTimestamp = &tnow
	}
	if tmp.MsgPackTimestamp.IsZero() {
		propertime := eventtime.GetEventTime(tmp.Time)
		tmp.MsgPackTimestamp = &propertime
	}

	*s = simpleSpan(tmp)
	return nil
}

func (s *simpleSpan) DebugString() string {
	return fmt.Sprintf("%#v", s)
}

// returns trace or log depending on the span information provdied
func (s *simpleSpan) SignalType() (string, error) {
	if sig, ok := s.Data["meta.signal_type"]; ok {
		switch sig {
		case "trace":
			if atype, ok := s.Data["meta.annotation_type"]; ok {
				if atype == "span_event" {
					return "span_event", nil
				} else if atype == "link" {
					return "span_link", nil
				}
				return "span", errors.New("invalid annotation type, but probably a span")
			}
			return "span", nil
		case "log":
			return "log", nil
		default:
			return "log", errors.New("Invalid meta.signal_type")
		}
	}
	return "log", errors.New("missing meta.signal_type and meta.annotation_type")
}

func (s *simpleSpan) GetService(cfg Config, seen *serviceHistory, dataset string) (string, error) {
	if serviceName, ok := s.Data[cfg.Resources.ServiceName]; ok {
		seen.NameCount[serviceName.(string)] += 1
		return serviceName.(string), nil
	}
	return dataset, errors.New("no service.name found in event")
}

func (s *simpleSpan) GetScope(cfg Config, seen *scopeHistory, serviceName string) (string, error) {
	if scopeLibraryName, ok := s.Data[cfg.Scopes.LibraryName]; ok {
		scopeKey := serviceName + scopeLibraryName.(string)
		if _, ok := seen.Scope[scopeKey]; ok {
			// if we've seen it, we don't expect it to be different right away so we'll just return it.
			return scopeKey, nil
		}
		// otherwise, we need to make a new found scope
		scopeLibraryVersion := "unset"
		if scopeLibVer, ok := s.Data[cfg.Scopes.LibraryVersion]; ok {
			scopeLibraryVersion = scopeLibVer.(string)
		}
		newScope := simpleScope{
			ServiceName:    serviceName, // we only set the service name once. If the same library comes from multiple services in the same batch, we're in trouble.
			LibraryName:    scopeLibraryName.(string),
			LibraryVersion: scopeLibraryVersion,
			ScopeSpans:     ptrace.NewSpanSlice(),
			ScopeLogs:      plog.NewLogRecordSlice(),
		}
		seen.Scope[scopeKey] = newScope
		return scopeKey, nil
	}
	return "libhoney.receiver", errors.New("No library name found")
}

type simpleScope struct {
	ServiceName    string
	LibraryName    string
	LibraryVersion string
	ScopeSpans     ptrace.SpanSlice
	ScopeLogs      plog.LogRecordSlice
}

type scopeHistory struct {
	Scope map[string]simpleScope // key here is service.name+library.name
}
type serviceHistory struct {
	NameCount map[string]int
}

// returns a ptrace.Span that is equivalent to the simpleSpan that came in already.
func (s *simpleSpan) ToPTraceSpan(newSpan *ptrace.Span, already_used_fields *[]string, cfg Config, logger zap.Logger) error {
	time_ns := s.MsgPackTimestamp.UnixNano()
	logger.Debug("processing trace with", zap.Int64("timestamp", time_ns))

	var parent_id trc.SpanID
	if pid, ok := s.Data[cfg.Attributes.ParentId]; ok {
		parent_id = SpanIDFrom(pid.(string))
		newSpan.SetParentSpanID(pcommon.SpanID(parent_id))
	}

	duration_ms := 0.0
	for _, df := range cfg.Attributes.DurationFields {
		if duration, okay := s.Data[df]; okay {
			duration_ms = duration.(float64)
			break
		}
	}
	end_timestamp := time_ns + (int64(duration_ms) * 1000000)

	if tid, ok := s.Data[cfg.Attributes.TraceId]; ok {
		tid := strings.Replace(tid.(string), "-", "", -1)
		if len(tid) > 32 {
			tid = tid[0:32]
		}
		newTraceId := pcommon.TraceID(TraceIDFrom(tid))
		newSpan.SetTraceID(newTraceId)
	} else {
		newSpan.SetTraceID(pcommon.TraceID(fakeMeAnId(32)))
	}

	if sid, ok := s.Data[cfg.Attributes.SpanId]; ok {
		sid := strings.Replace(sid.(string), "-", "", -1)
		if len(sid) == 32 {
			sid = sid[8:24]
		} else if len(sid) > 16 {
			sid = sid[0:16]
		}
		newSpanId := pcommon.SpanID(SpanIDFrom(sid))
		newSpan.SetSpanID(newSpanId)
	} else {
		newSpan.SetSpanID(pcommon.SpanID(fakeMeAnId(16)))
	}

	newSpan.SetStartTimestamp(pcommon.Timestamp(time_ns))
	newSpan.SetEndTimestamp(pcommon.Timestamp(end_timestamp))

	if spanName, ok := s.Data[cfg.Attributes.Name]; ok {
		newSpan.SetName(spanName.(string))
	}
	newSpan.Status().SetCode(ptrace.StatusCodeOk)

	if _, ok := s.Data[cfg.Attributes.Error]; ok {
		newSpan.Status().SetCode(ptrace.StatusCodeError)
	}

	if spanKind, ok := s.Data[cfg.Attributes.SpanKind]; ok {
		switch spanKind.(string) {
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
	}

	newSpan.Attributes().PutInt("SampleRate", int64(s.Samplerate))

	for k, v := range s.Data {
		if slices.Contains(*already_used_fields, k) {
			continue
		}
		switch v := v.(type) {
		case string:
			newSpan.Attributes().PutStr(k, v)
		case int:
			newSpan.Attributes().PutInt(k, int64(v))
		case int64, int16, int32:
			intv := v.(int64)
			newSpan.Attributes().PutInt(k, intv)
		case float64:
			newSpan.Attributes().PutDouble(k, v)
		case bool:
			newSpan.Attributes().PutBool(k, v)
		default:
			logger.Warn("Span data type issue", zap.String("trace.trace_id", newSpan.TraceID().String()), zap.String("trace.span_id", newSpan.SpanID().String()), zap.String("key", k))
		}
	}
	return nil
}

func (s *simpleSpan) ToPLogRecord(newLog *plog.LogRecord, already_used_fields *[]string, cfg Config, logger zap.Logger) error {
	time_ns := s.MsgPackTimestamp.UnixNano()
	logger.Debug("processing log with", zap.Int64("timestamp", time_ns))

	newLog.SetTimestamp(pcommon.Timestamp(time_ns))

	// set trace ID if it exists (Husky calls it "trace.trace_id")
	if tid, ok := s.Data[cfg.Attributes.TraceId]; ok {
		tid := strings.Replace(tid.(string), "-", "", -1)
		if len(tid) > 32 {
			tid = tid[0:32]
		}
		newTraceId := pcommon.TraceID(TraceIDFrom(tid))
		newLog.SetTraceID(newTraceId)
	}
	// set a span ID if it exists (Husky calls it "trace.parent_id")
	if sid, ok := s.Data[cfg.Attributes.ParentId]; ok {
		sid := strings.Replace(sid.(string), "-", "", -1)
		if len(sid) == 32 {
			sid = sid[8:24]
		} else if len(sid) > 16 {
			sid = sid[0:16]
		}
		newSpanId := pcommon.SpanID(SpanIDFrom(sid))
		newLog.SetSpanID(newSpanId)
	}

	if logSevCode, ok := s.Data["severity_code"]; ok {
		logSevInt := int32(logSevCode.(int64))
		newLog.SetSeverityNumber(plog.SeverityNumber(logSevInt))
	}

	if logSevText, ok := s.Data["severity_text"]; ok {
		newLog.SetSeverityText(logSevText.(string))
	}

	if logFlags, ok := s.Data["flags"]; ok {
		logFlagsUint := uint32(logFlags.(uint64))
		newLog.SetFlags(plog.LogRecordFlags(logFlagsUint))
	}

	// undoing this is gonna be complicated: https://github.com/honeycombio/husky/blob/91c0498333cd9f5eed1fdb8544ca486db7dea565/otlp/logs.go#L61
	if logBody, ok := s.Data["body"]; ok {
		newLog.Body().SetStr(logBody.(string))
	}

	newLog.Attributes().PutInt("SampleRate", int64(s.Samplerate))

	logFieldsAlready := []string{"severity_text", "severity_code", "flags", "body"}
	for k, v := range s.Data {
		if slices.Contains(*already_used_fields, k) {
			continue
		}
		if slices.Contains(logFieldsAlready, k) {
			continue
		}
		switch v := v.(type) {
		case string:
			newLog.Attributes().PutStr(k, v)
		case int:
			newLog.Attributes().PutInt(k, int64(v))
		case int64, int16, int32:
			intv := v.(int64)
			newLog.Attributes().PutInt(k, intv)
		case float64:
			newLog.Attributes().PutDouble(k, v)
		case bool:
			newLog.Attributes().PutBool(k, v)
		default:
			logger.Warn("Span data type issue", zap.Int64("timestamp", time_ns), zap.String("key", k))
		}
	}
	return nil
}

func toPsomething(dataset string, ss []simpleSpan, cfg Config, logger zap.Logger) (ptrace.Traces, plog.Logs, error) {
	foundServices := serviceHistory{}
	foundServices.NameCount = make(map[string]int)
	foundScopes := scopeHistory{}
	foundScopes.Scope = make(map[string]simpleScope)

	foundScopes.Scope = make(map[string]simpleScope)                                                                                             // a list of already seen scopes
	foundScopes.Scope["libhoney.receiver"] = simpleScope{dataset, "libhoney.receiver", "1.0.0", ptrace.NewSpanSlice(), plog.NewLogRecordSlice()} // seed a default

	spanLinks := map[trc.SpanID][]simpleSpan{}
	spanEvents := map[trc.SpanID][]simpleSpan{}

	already_used_fields := []string{cfg.Resources.ServiceName, cfg.Scopes.LibraryName, cfg.Scopes.LibraryVersion}
	already_used_fields = append(already_used_fields, cfg.Attributes.Name,
		cfg.Attributes.TraceId, cfg.Attributes.ParentId, cfg.Attributes.SpanId,
		cfg.Attributes.Error, cfg.Attributes.SpanKind,
	)
	already_used_fields = append(already_used_fields, cfg.Attributes.DurationFields...)

	for _, span := range ss {
		// logger.Debug("Print span contents", zap.String("span.object", span.DebugString()))

		var parent_id trc.SpanID
		if pid, ok := span.Data[cfg.Attributes.ParentId]; ok {
			parent_id = SpanIDFrom(pid.(string))
		}
		// main switch to do the thing
		action, err := span.SignalType()
		if err != nil {
			logger.Warn("signal type unclear")
		}
		switch action {
		case "span":
			spanService, _ := span.GetService(cfg, &foundServices, dataset)
			spanScopeKey, _ := span.GetScope(cfg, &foundScopes, spanService) //adds a new found scope if needed
			newSpan := foundScopes.Scope[spanScopeKey].ScopeSpans.AppendEmpty()
			err := span.ToPTraceSpan(&newSpan, &already_used_fields, cfg, logger)
			if err != nil {
				logger.Warn("span could not be converted from libhoney to ptrace", zap.String("span.object", span.DebugString()))
			}
		case "log":
			logService, _ := span.GetService(cfg, &foundServices, dataset)
			logScopeKey, _ := span.GetScope(cfg, &foundScopes, logService) //adds a new found scope if needed
			newLog := foundScopes.Scope[logScopeKey].ScopeLogs.AppendEmpty()
			span.ToPLogRecord(&newLog, &already_used_fields, cfg, logger)
			if err != nil {
				logger.Warn("log could not be converted from libhoney to plog", zap.String("span.object", span.DebugString()))
			}
		case "span_event":
			spanEvents[parent_id] = append(spanEvents[parent_id], span)
		case "span_link":
			spanLinks[parent_id] = append(spanLinks[parent_id], span)
		}
	}

	// add span links and events back
	// i'd like to only loop through the span events and links arrays but don't see a way to address the slices
	start := time.Now()
	for _, ss := range foundScopes.Scope {
		for i := 0; i < ss.ScopeSpans.Len(); i++ {
			sp := ss.ScopeSpans.At(i)

			spId := trc.SpanID(sp.SpanID())
			if speArr, ok := spanEvents[spId]; ok {
				for _, spe := range speArr {
					newEvent := sp.Events().AppendEmpty()
					newEvent.SetTimestamp(pcommon.Timestamp(spe.MsgPackTimestamp.UnixNano()))
					newEvent.SetName(spe.Data["name"].(string))
					for lkey, lval := range spe.Data {
						if slices.Contains(already_used_fields, lkey) {
							continue
						}
						if lkey == "meta.annotation_type" || lkey == "meta.signal_type" {
							continue
						}
						switch lval := lval.(type) {
						case string:
							newEvent.Attributes().PutStr(lkey, lval)
						case int:
							newEvent.Attributes().PutInt(lkey, int64(lval))
						case int64, int16, int32:
							intv := lval.(int64)
							newEvent.Attributes().PutInt(lkey, intv)
						case float64:
							newEvent.Attributes().PutDouble(lkey, lval)
						case bool:
							newEvent.Attributes().PutBool(lkey, lval)
						default:
							logger.Error("SpanEvent data type issue", zap.String("trace.trace_id", sp.TraceID().String()), zap.String("trace.span_id", sp.SpanID().String()), zap.String("key", lkey))
						}
					}
				}
			}
			if splArr, ok := spanLinks[spId]; ok {
				for _, spl := range splArr {
					newLink := sp.Links().AppendEmpty()

					var linkTraceId trc.TraceID
					if linkTraceStr, ok := spl.Data["trace.link.trace_id"]; ok {
						linkTraceId = TraceIDFrom(linkTraceStr.(string))
						newLink.SetTraceID(pcommon.TraceID(linkTraceId))
					} else {
						logger.Warn("span link missing attributes", zap.String("missing.attribute", "trace.link.trace_id"), zap.String("span link contents", spl.DebugString()))
						continue
					}
					var linkSpanId trc.SpanID
					if linkSpanStr, ok := spl.Data["trace.link.span_id"]; ok {
						linkSpanId = SpanIDFrom(linkSpanStr.(string))
						newLink.SetSpanID(pcommon.SpanID(linkSpanId))
					} else {
						logger.Warn("span link missing attributes", zap.String("missing.attribute", "trace.link.span_id"), zap.String("span link contents", spl.DebugString()))
						continue
					}
					for lkey, lval := range spl.Data {

						if len(lkey) > 10 && lkey[:11] == "trace.link." {
							continue
						}
						if slices.Contains(already_used_fields, lkey) {
							continue
						}
						if lkey == "meta.annotation_type" || lkey == "meta.signal_type" {
							continue
						}
						switch lval := lval.(type) {
						case string:
							newLink.Attributes().PutStr(lkey, lval)
						case int:
							newLink.Attributes().PutInt(lkey, int64(lval))
						case int64, int16, int32:
							intv := lval.(int64)
							newLink.Attributes().PutInt(lkey, intv)
						case float64:
							newLink.Attributes().PutDouble(lkey, lval)
						case bool:
							newLink.Attributes().PutBool(lkey, lval)
						default:
							logger.Error("SpanLink data type issue", zap.String("trace.trace_id", sp.TraceID().String()), zap.String("trace.span_id", sp.SpanID().String()), zap.String("key", lkey))
						}
					}
				}
			}
		}
	}
	logger.Debug("time to reattach span events and links", zap.Duration("duration", time.Since(start)))

	resultTraces := ptrace.NewTraces()
	resultLogs := plog.NewLogs()

	for scopeName, ss := range foundScopes.Scope {
		// make a slice and load it up with scopes
		if ss.ScopeSpans.Len() > 0 {
			rs := resultTraces.ResourceSpans().AppendEmpty()
			rs.SetSchemaUrl(semconv.SchemaURL)
			rs.Resource().Attributes().PutStr(semconv.AttributeServiceName, ss.ServiceName)

			in := rs.ScopeSpans().AppendEmpty()
			in.Scope().SetName(ss.LibraryName)
			in.Scope().SetVersion(ss.LibraryVersion)
			foundScopes.Scope[scopeName].ScopeSpans.MoveAndAppendTo(in.Spans())
		}
		if ss.ScopeLogs.Len() > 0 {
			lr := resultLogs.ResourceLogs().AppendEmpty()
			lr.SetSchemaUrl(semconv.SchemaURL)
			lr.Resource().Attributes().PutStr(semconv.AttributeServiceName, ss.ServiceName)

			ls := lr.ScopeLogs().AppendEmpty()
			ls.Scope().SetName(ss.LibraryName)
			ls.Scope().SetVersion(ss.LibraryVersion)
			foundScopes.Scope[scopeName].ScopeLogs.MoveAndAppendTo(ls.LogRecords())
		}
	}

	return resultTraces, resultLogs, nil
}
