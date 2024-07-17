// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// copied into this receiver. not sure if including by reference is a better idea.
package libhoneyreceiver // import "go.opentelemetry.io/collector/receiver/otlpreceiver"

import (
	"bytes"

	"github.com/gogo/protobuf/jsonpb"
	spb "google.golang.org/genproto/googleapis/rpc/status"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

const (
	pbContentType      = "application/x-protobuf"
	jsonContentType    = "application/json"
	msgpackContentType = "application/x-msgpack"
)

var (
	jsEncoder       = &jsonEncoder{}
	jsonPbMarshaler = &jsonpb.Marshaler{}
	mpEncoder       = &msgpackEncoder{}
)

type encoder interface {
	unmarshalTracesRequest(buf []byte) (ptraceotlp.ExportRequest, error)
	unmarshalMetricsRequest(buf []byte) (pmetricotlp.ExportRequest, error)
	unmarshalLogsRequest(buf []byte) (plogotlp.ExportRequest, error)

	marshalTracesResponse(ptraceotlp.ExportResponse) ([]byte, error)
	marshalMetricsResponse(pmetricotlp.ExportResponse) ([]byte, error)
	marshalLogsResponse(plogotlp.ExportResponse) ([]byte, error)

	marshalStatus(rsp *spb.Status) ([]byte, error)

	contentType() string
}

type protoEncoder struct{}

func (protoEncoder) unmarshalTracesRequest(buf []byte) (ptraceotlp.ExportRequest, error) {
	req := ptraceotlp.NewExportRequest()
	err := req.UnmarshalProto(buf)
	return req, err
}

func (protoEncoder) unmarshalMetricsRequest(buf []byte) (pmetricotlp.ExportRequest, error) {
	req := pmetricotlp.NewExportRequest()
	err := req.UnmarshalProto(buf)
	return req, err
}

func (protoEncoder) unmarshalLogsRequest(buf []byte) (plogotlp.ExportRequest, error) {
	req := plogotlp.NewExportRequest()
	err := req.UnmarshalProto(buf)
	return req, err
}

type jsonEncoder struct{}

func (jsonEncoder) unmarshalTracesRequest(buf []byte) (ptraceotlp.ExportRequest, error) {
	req := ptraceotlp.NewExportRequest()
	err := req.UnmarshalJSON(buf)
	return req, err
}

func (jsonEncoder) unmarshalMetricsRequest(buf []byte) (pmetricotlp.ExportRequest, error) {
	req := pmetricotlp.NewExportRequest()
	err := req.UnmarshalJSON(buf)
	return req, err
}

func (jsonEncoder) unmarshalLogsRequest(buf []byte) (plogotlp.ExportRequest, error) {
	req := plogotlp.NewExportRequest()
	err := req.UnmarshalJSON(buf)
	return req, err
}

func (jsonEncoder) marshalTracesResponse(resp ptraceotlp.ExportResponse) ([]byte, error) {
	return resp.MarshalJSON()
}

func (jsonEncoder) marshalMetricsResponse(resp pmetricotlp.ExportResponse) ([]byte, error) {
	return resp.MarshalJSON()
}

func (jsonEncoder) marshalLogsResponse(resp plogotlp.ExportResponse) ([]byte, error) {
	return resp.MarshalJSON()
}

func (jsonEncoder) marshalStatus(resp *spb.Status) ([]byte, error) {
	buf := new(bytes.Buffer)
	err := jsonPbMarshaler.Marshal(buf, resp)
	return buf.Bytes(), err
}

func (jsonEncoder) contentType() string {
	return jsonContentType
}

type msgpackEncoder struct{}

// not really implemented because these aren't used by the libhoney receiver yet.
func (msgpackEncoder) unmarshalTracesRequest(buf []byte) (ptraceotlp.ExportRequest, error) {
	req := ptraceotlp.NewExportRequest()
	err := req.UnmarshalJSON(buf)
	return req, err
}

func (msgpackEncoder) unmarshalMetricsRequest(buf []byte) (pmetricotlp.ExportRequest, error) {
	req := pmetricotlp.NewExportRequest()
	err := req.UnmarshalJSON(buf)
	return req, err
}

func (msgpackEncoder) unmarshalLogsRequest(buf []byte) (plogotlp.ExportRequest, error) {
	req := plogotlp.NewExportRequest()
	err := req.UnmarshalJSON(buf)
	return req, err
}

func (msgpackEncoder) marshalTracesResponse(resp ptraceotlp.ExportResponse) ([]byte, error) {
	return resp.MarshalJSON()
}

func (msgpackEncoder) marshalMetricsResponse(resp pmetricotlp.ExportResponse) ([]byte, error) {
	return resp.MarshalJSON()
}

func (msgpackEncoder) marshalLogsResponse(resp plogotlp.ExportResponse) ([]byte, error) {
	return resp.MarshalJSON()
}

func (msgpackEncoder) marshalStatus(resp *spb.Status) ([]byte, error) {
	buf := new(bytes.Buffer)
	err := jsonPbMarshaler.Marshal(buf, resp)
	return buf.Bytes(), err
}

func (msgpackEncoder) contentType() string {
	return msgpackContentType
}
