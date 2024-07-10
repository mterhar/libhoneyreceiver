// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package libhoneyreceiver // import "go.opentelemetry.io/collector/receiver/otlpreceiver"

import (
	"context"
	"fmt"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/metadata"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/sharedcomponent"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

const (
	httpPort = 8080
)

var defaultTracesURLPaths = []string{"/events", "/event", "/batch"}

// NewFactory creates a new OTLP receiver factory.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithTraces(createTraces, metadata.TracesStability),
	)
}

// createDefaultConfig creates the default configuration for receiver.
func createDefaultConfig() component.Config {
	durationFieldsArr := []string{"duration_ms"}
	endpointStr := fmt.Sprintf("localhost:%d", httpPort)
	return &Config{
		HTTP: &HTTPConfig{
			ServerConfig: &confighttp.ServerConfig{
				Endpoint: endpointStr,
			},
			TracesURLPaths: defaultTracesURLPaths,
		},
		Attributes: AttributesConfig{
			TraceId:        "data.trace.trace_id",
			SpanId:         "data.trace.span_id",
			ParentId:       "data.trace.parent_id",
			Name:           "data.name",
			SpanKind:       "data.span.kind",
			DurationFields: durationFieldsArr,
		},
	}
}

// createTraces creates a trace receiver based on provided config.
func createTraces(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	nextConsumer consumer.Traces,
) (receiver.Traces, error) {
	oCfg := cfg.(*Config)
	r, err := receivers.LoadOrStore(
		oCfg,
		func() (*libhoneyReceiver, error) {
			return newLibhoneyReceiver(oCfg, &set)
		},
		&set.TelemetrySettings,
	)
	if err != nil {
		return nil, err
	}

	r.Unwrap().registerTraceConsumer(nextConsumer)
	return r, nil
}

// This is the map of already created OTLP receivers for particular configurations.
// We maintain this map because the Factory is asked trace and metric receivers separately
// when it gets CreateTracesReceiver() and CreateMetricsReceiver() but they must not
// create separate objects, they must use one otlpReceiver object per configuration.
// When the receiver is shutdown it should be removed from this map so the same configuration
// can be recreated successfully.
var receivers = sharedcomponent.NewMap[*Config, *libhoneyReceiver]()
