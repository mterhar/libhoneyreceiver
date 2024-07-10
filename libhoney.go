// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package libhoneyreceiver // import "github.com/mterhar/arbitraryjsonreceiver"

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/trace"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
)

// arbitraryjsonReceiver is the type that exposes Trace and Metrics reception.
type libhoneyReceiver struct {
	cfg        *Config
	serverHTTP *http.Server

	nextTraces consumer.Traces
	shutdownWG sync.WaitGroup

	obsrepHTTP *receiverhelper.ObsReport

	settings *receiver.Settings
}

// newLibhoneyReceiver just creates the OpenTelemetry receiver services. It is the caller's
// responsibility to invoke the respective Start*Reception methods as well
// as the various Stop*Reception methods to end it.
func newLibhoneyReceiver(cfg *Config, set *receiver.Settings) (*libhoneyReceiver, error) {
	r := &libhoneyReceiver{
		cfg:        cfg,
		nextTraces: nil,
		settings:   set,
	}

	var err error
	r.obsrepHTTP, err = receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             set.ID,
		Transport:              "http",
		ReceiverCreateSettings: *set,
	})
	if err != nil {
		return nil, err
	}

	return r, nil
}

func (r *libhoneyReceiver) startHTTPServer(ctx context.Context, host component.Host) error {
	// If HTTP is not enabled, nothing to start.
	if r.cfg.HTTP == nil {
		return nil
	}

	httpMux := http.NewServeMux()
	if r.nextTraces != nil {
		httpTracesReceiver := trace.New(r.nextTraces, r.obsrepHTTP)
		for _, path := range r.cfg.HTTP.TracesURLPaths {
			httpMux.HandleFunc(path, func(resp http.ResponseWriter, req *http.Request) {
				handleTraces(resp, req, httpTracesReceiver, *r.cfg)
				r.settings.Logger.Info("Adding path to HTTP server", zap.String("path", path))

			})
		}
	}

	var err error
	if r.serverHTTP, err = r.cfg.HTTP.ToServer(ctx, host, r.settings.TelemetrySettings, httpMux, confighttp.WithErrorHandler(errorHandler)); err != nil {
		return err
	}

	r.settings.Logger.Info("Starting HTTP server", zap.String("endpoint", r.cfg.HTTP.ServerConfig.Endpoint))
	var hln net.Listener
	if hln, err = r.cfg.HTTP.ServerConfig.ToListener(ctx); err != nil {
		return err
	}

	r.shutdownWG.Add(1)
	go func() {
		defer r.shutdownWG.Done()

		if errHTTP := r.serverHTTP.Serve(hln); errHTTP != nil && !errors.Is(errHTTP, http.ErrServerClosed) {
			r.settings.ReportStatus(component.NewFatalErrorEvent(errHTTP))
		}
	}()
	return nil
}

// Start runs the trace receiver on the gRPC server. Currently
// it also enables the metrics receiver too.
func (r *libhoneyReceiver) Start(ctx context.Context, host component.Host) error {

	if err := r.startHTTPServer(ctx, host); err != nil {
		return errors.Join(err, r.Shutdown(ctx))
	}

	return nil
}

// Shutdown is a method to turn off receiving.
func (r *libhoneyReceiver) Shutdown(ctx context.Context) error {
	var err error

	if r.serverHTTP != nil {
		err = r.serverHTTP.Shutdown(ctx)
	}

	r.shutdownWG.Wait()
	return err
}

func (r *libhoneyReceiver) registerTraceConsumer(tc consumer.Traces) {
	r.nextTraces = tc
}
