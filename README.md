# Libhoney Receiver

<!-- status autogenerated section -->
[alpha]: https://github.com/open-telemetry/opentelemetry-collector#alpha
[core]: https://github.com/open-telemetry/opentelemetry-collector-releases/tree/main/distributions/otelcol
[contrib]: https://github.com/open-telemetry/opentelemetry-collector-releases/tree/main/distributions/otelcol-contrib
<!-- end autogenerated section -->

Libhoney receiver takes trace data that is in emitted from Honeycomb's Refinery proxy and converts them into OpenTelemetry.

From Refinery, span information comes from a few sources:

1. A header exist: `x-honeycomb-team` which is an API key
2. A path element exists: `/events/{dataset_name}` which is analogous to `service.name` resource attribute in OTLP
3. A webpack or json encoded body

There is no support for span links, span events, metrics, or logs at this point.

If span links and span events are flowing through this back into Honeycomb, they'll look like they always do in Honeycomb.
It doesn't currently reattach span links or events in to the span's child arrays.

## Getting Started

The following settings are required:

- `http`
  - `endpoint` must set an endpoint. Defaults to `127.0.0.1:8080`
- `resources`: if the `service.name` field is different, map it here.
- `scopes`: to get the `library.name` and `library.version` set in the scope section, set them here.
- `attributes`: If the other trace-related data have different keys, map them here, defautls are otlp-like field names.

The following setting is required for refinery traffic since:

- `auth_api`: should be set to `https://api.honeycomb.io` or a proxy that forwards to that host.
  Refinery checks with the `/1/auth` endpoint to get environment names so it needs to be passed through.

Example:

```yaml
libhoney:
    http:
      endpoint: 0.0.0.0:8088
      traces_url_paths:
        - "/1/events"
        - "/1/batch"
      include_metadata: true
    auth_api: https://api.honeycomb.io
    resources:
      service_name: service_name
    scopes:
      library_name: library.name
      library_version: library.version
    attributes:
      trace_id: trace_id
      parent_id: parent_id
      span_id: span_id
      name: name
      error: error
      spankind: span.kind
      durationFields:
        - duration_ms
```

## API key handling requires Headers Setter Extension

Be sure to use that extension to pass the `x-honeycomb-team` header through from the receiver to the exporter.

## Timestamps

The `time` field in the root of the JSON sets the timestamp. It tries to interpret it as `time.RFC3339Nano`, then as a unix timestamp integer with seconds, milliseconds, or nanoseconds based on the number of orders of magnitude. 

If the `time` field is missing, it uses `time.Now()` which shouldn't ever happen because upstream Refinery will have already done this.

## Unsupported stuff

There are several aspects of opentelemtry that aren't or can't be supported because we are going from a low-fidelity signal to a higher fidelity signal.  OTLP is more expressive what Refinery emits as of 2.6.1.

### No span events or links

These are children of spans in a separate array in OTLP. Some backends may be able to have span events or span links replicated but this receiver won't try to make them back into OTLP-style child arrays.

### Set arbitrary values

To set values in these spans, use the Transform processor.

### String trace and span identifiers

If these fields cannot be converted into hex, they get truncated or a new hex value derived from the incompatible string is used.

## Usage

Configure refinery to point to the collector. Send spans through Refinery using an application or curl.
