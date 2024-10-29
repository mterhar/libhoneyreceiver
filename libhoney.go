// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
package libhoneyreceiver // import "github.com/mterhar/arbitraryjsonreceiver"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"go.uber.org/zap"

	logs "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/logs"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/libhoneyreceiver/internal/trace"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
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
	nextLogs   consumer.Logs
	shutdownWG sync.WaitGroup

	obsrepHTTP *receiverhelper.ObsReport

	settings *receiver.Settings
}

type TeamInfo struct {
	Slug string `json:"slug"`
}

type EnvironmentInfo struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type AuthInfo struct {
	APIKeyAccess map[string]bool `json:"api_key_access"`
	Team         TeamInfo        `json:"team"`
	Environment  EnvironmentInfo `json:"environment"`
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
		httpLogsReceiver := logs.New(r.nextLogs, r.obsrepHTTP)

		r.settings.Logger.Info("r.nextTraces is not null so httpTracesReciever was added", zap.Int("paths", len(r.cfg.HTTP.TracesURLPaths)))
		for _, path := range r.cfg.HTTP.TracesURLPaths {
			httpMux.HandleFunc(path, func(resp http.ResponseWriter, req *http.Request) {
				handleSomething(resp, req, httpTracesReceiver, httpLogsReceiver, *r.cfg)
			})
			r.settings.Logger.Debug("Added path to HTTP server", zap.String("path", path))
		}

		// borrowed the auth stuff from https://github.com/honeycombio/refinery/blob/e4c23d2c747ea2ef910229e900ca859b24ac7cbc/route/route.go#L896
		if r.cfg.AuthApi != "" {
			httpMux.HandleFunc("/1/auth", func(resp http.ResponseWriter, req *http.Request) {
				authURL := fmt.Sprintf("%s/1/auth", r.cfg.AuthApi)
				authReq, err := http.NewRequest(http.MethodGet, authURL, nil)
				if err != nil {
					errJson, _ := json.Marshal(`{"error": "failed to create AuthInfo request"}`)
					writeResponse(resp, "json", http.StatusBadRequest, errJson)
					return
				}
				authReq.Header.Set("x-honeycomb-team", req.Header.Get("x-honeycomb-team"))
				var authClient http.Client
				authResp, err := authClient.Do(authReq)
				if err != nil {
					errJson, _ := json.Marshal(fmt.Sprintf(`"error": "failed to send request to auth api endpoint", "message", "%s"}`, err.Error()))
					writeResponse(resp, "json", http.StatusBadRequest, errJson)
					return
				}
				defer authResp.Body.Close()

				switch {
				case authResp.StatusCode == http.StatusUnauthorized:
					errJson, _ := json.Marshal(`"error": "received 401 response for AuthInfo request from Honeycomb API - check your API key"}`)
					writeResponse(resp, "json", http.StatusBadRequest, errJson)
					return
				case authResp.StatusCode > 299:
					errJson, _ := json.Marshal(fmt.Sprintf(`"error": "bad response code from API", "status_code", %d}`, authResp.StatusCode))
					writeResponse(resp, "json", http.StatusBadRequest, errJson)
					return
				}
				authRawBody, _ := io.ReadAll(authResp.Body)
				resp.Write(authRawBody)
			})
		}
	} else {
		r.settings.Logger.Debug("r.nextTraces is nil for some reason")
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
			componentstatus.ReportStatus(host, componentstatus.NewFatalErrorEvent(errHTTP))
		}
	}()
	return nil
}

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

func (r *libhoneyReceiver) registerLosConsumer(tc consumer.Logs) {
	r.nextLogs = tc
}
