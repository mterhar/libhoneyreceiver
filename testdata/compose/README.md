# Testing Setup

## Directory structure

* root (`go.work` file)
  * libhoney (git clone this repo)
    * testdata
      * compose (this directory)
  * otelcol-dev (from the otel collector builder output)

In the root directory, I have `ocb` for otel collector building and `loadgen` for creating libhoney events.

## Connectivity path

1. `loadgen --host=local --dataset=loadgen --apikey=hcaik...` > localhost:8889 which is a docker-compose port.
1. docker-compose maps host port 8889 to Refinery container's port 8080
1. Refinery config `Network.ListenAddr: 0.0.0.0:8080` receives the traffic and processes it
1. Refinery config `Network.HoneycombAPI: http://LOCAL_NETWORK_IP:8088` passes spans to the collector
1. from the root directory, `go run ./otelcol-dev --config ./libhoney/testdata/otel_config_libhoney.yaml` receives on 0.0.0.0:8088
1. processes traces and sends them to exporter
1. headers_setter extension drags API key from receiver to exporter
1. exporter delivers spans to Honeycomb using OTLP but the spans are kinda honeycomb-looking

## Setting up the otelcol-dev directory

The expected otelcol-dev directory is what you get after following the instructions
[here](https://opentelemetry.io/docs/collector/building/receiver/).

I used this `builder-config.yaml`

```yaml
dist:
  name: otelcol-dev
  description: Libhoney testing collector
  output_path: ./otelcol-dev
  version: 0.104.0
  otelcol_version: 0.104.0

extensions:
  - gomod: go.opentelemetry.io/collector/extension/zpagesextension v0.104.0
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/extension/basicauthextension v0.104.0
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/extension/bearertokenauthextension v0.104.0
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/extension/headerssetterextension v0.104.0
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/extension/healthcheckextension v0.104.0

exporters:
  - gomod: go.opentelemetry.io/collector/exporter/debugexporter v0.104.0
  - gomod: go.opentelemetry.io/collector/exporter/otlpexporter v0.104.0
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/exporter/fileexporter v0.104.0

processors:
  - gomod: go.opentelemetry.io/collector/processor/batchprocessor v0.104.0
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/processor/transformprocessor v0.104.0

receivers:
  - gomod: go.opentelemetry.io/collector/receiver/otlpreceiver v0.104.0

connectors:
  - gomod: go.opentelemetry.io/collector/connector/forwardconnector v0.104.0
  - gomod: github.com/open-telemetry/opentelemetry-collector-contrib/connector/routingconnector v0.104.0
```

And manually added the libhoney receiver to the `components.go` file as suggested in the dock referenced above.

Root's `go.work` file:

```go
go 1.21.11

use (
    ./libhoney
    ./otelcol-dev
)
```

## Setting up the libhoney directory

Just git clone the repo into there.

## Running the collector

Since the whole libhoney repo is just called by the oteldev-col repo, building it is a bit funky.

In the root directory, run `go run ./otelcol-dev --config ./libhoney/testdata/otel_config_libhoney.yaml`

This should start it up with enough configurations to pass libhoney events through.

## Test pipeline

Get the `honeycombio/loadgen` app to test libhoney events. 

```shell
loadgen --host=local --dataset=loadgen --apikey=hcaik...
```

You can send these directly through the collector to see if it will pass data along.

You can also send these through the test pipeline since Refinery does stuff a bit differently.

### Start Refinery

Edit the `./libhoney/testdata/compose/docker-compose.yml` file so it has API key.

Edit the `./libhoney/testdata/compose/config.yaml` file so it has the right host IP address in `HoneycombAPI: http://!!HERE!!:8088`

Note that the rules file doesn't do any sampling but it does add some `meta.refinery...` attributes.

In the `./libhoney/testdata/compose` directory `run docker compose up` to get a Refinery node running.

### Send some otel data

```shell
loadgen --host=http://localhost:4317 --apikey=hcaik_... --dataset=loadgen --tracecount=4 --loglevel=debug --sender=otel --insecure service.name=loadgen-otel
```

## Troubleshooting attributes

Currently some troubleshooting attributes are added to span that it's parsing.

1. `libhoney.receiver.service_name` shows what came in that matched the service_name configuration.
1. `libhoney.receiver.dataset` shows what came in as the dataset component in the path
1. `libhoney.receiver.library_name` and `libhoney.receiver.library_vesion` which I probably have to create instrumentationScopes for but haven't tested with robust enough information yet
