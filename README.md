# ArbitraryJSON Receiver

<!-- status autogenerated section -->
[alpha]: https://github.com/open-telemetry/opentelemetry-collector#alpha
[core]: https://github.com/open-telemetry/opentelemetry-collector-releases/tree/main/distributions/otelcol
[contrib]: https://github.com/open-telemetry/opentelemetry-collector-releases/tree/main/distributions/otelcol-contrib
<!-- end autogenerated section -->

Arbirary JSON receiver takes trace data that is in a different JSON format as HTTP requests. These JSON spans are assumed to be one span per JSON object.

There are 2 aspects of the configuration, explicit mappings for fields needed for the protocol and an extra "attributes from" array of strings that will bring in any attributes from the JSON.

For example, take the following JSON object.

```json
[
  {
    "samplerate": 30,
    "time": 1719858203,
    "service.name": "MikeServer",
    "data": {
        "name": "Span Name",
        "trace.trace_id": "3923a372ed02308",
        "trace.span_id": "390230ffc3",
        "random.data": 490,
        "random.other": 320,
        "duration_ms": 3932
    }
  },
  {
    // another span ...
  }
]
```

In this case, the explicit mappings would handle sample rate, service name, trace and span identifiers, timestamps, and the span name. The other 2 fields would be mapped by telling it to pull `attributes_from: [ "data" ]`.

There is no support for metrics or logs.

## Getting Started

The following settings are required:

- `http`
  - `endpoint` must set an endpoint. Defaults to `127.0.0.1:8080`
- `wrapper` can be:
  - `none` for nothing (just lots of json objects in a file) which is technically invalid json
  - `array` for a simple `[ {}, {}, {} ]` format
  - a string that is a path to where the spans are
- `resources`: if the `service.name` field is different, map it here.
- `attributes`: If the other trace-related data have different keys, map them here, defautls are OTLP field names.
- `attributes_from`: Point to the JSON key that contains all the attributes, default is `data`

The following settings can be optionally configured:

- `not sure yet`

Example:

```yaml
receivers:
  arbitraryjson:
```

## Timestamps

The receiver can be configured to use either a start and end timestamp like how OTLP stores the data or a timestamp and duration.

For systems that use different names for the timestamp and duration fields, each of those configurations is an array so multiple keys can be used.

It prefers start and end timestamps. If one is missing, it will try to use the duration to create the other one.

## Unsupported stuff

There are several aspects of opentelemtry that aren't or can't be supported because we are going from a low-fidelity signal to a higher fidelity signal.  OTLP is way more expressive than raw JSON so we don't know what types are, what resources or scopes are, etc.

### No span events or links

These are children of spans in a separate array in OTLP. Some backends may be able to have span events or span links replicated but this receiver won't try to make them back into OTLP-style child arrays. 

### Set arbitrary values

To set values in these spans, use the Transform processor.

### String trace and span identifiers

If these fields cannot be converted into hex, they're just gonna get set randomly which sucks.

TODO: Maybe I could find a way to hash and truncate.

## Usage

Use `jq` to test paths in a file.

To find all of the trace IDs to see if they're in a compatible format, you can use the
command: `jq '.data."trace.trace_id"' tracedata.json`

After configuring the schema and such, do some of this:

```shell
curl -X POST -H "Content-Type: application/json" \
  --data-binary @tracedata.json \
  -i http://localhost:8080
```

