# Advanced Script Exporter

Based on GitHub: https://github.com/adhocteam/script_exporter
What was changed:
- Added new metric `script_exit_code`
- Added labels `nagios_status`, `timeout` and `output` to all merics

Prometheus exporter written to execute and collect metrics on script exit status
and duration. Designed to allow the execution of probes where support for the
probe type wasn't easily configured with the Prometheus blackbox exporter.

Minimum supported Go Version: 1.13.1

## Sample Configuration

```yaml
scripts:
  - name: success
    script: sleep 5

  - name: failure
    script: sleep 2 && exit 1

  - name: timeout
    script: sleep 5
    timeout: 1
```

## Running

You can run via docker with:

```
docker run -d -p 9172:9172 --name script-exporter \
  -v `pwd`/script-exporter.yml:/etc/script-exporter/config.yml:ro \
  konstantin-kornienko-epam/script_advanced_exporter:master \
  -config.file=/etc/script-exporter/config.yml \
  -web.listen-address=":9172" \
  -web.telemetry-path="/metrics" \
  -config.shell="/bin/sh"
```

You'll need to customize the docker image or use the binary on the host system
to install tools such as curl for certain scenarios.

## Probing

To return the script exporter internal metrics exposed by the default Prometheus
handler:

`$ curl http://localhost:9172/metrics`

To execute a script, use the `name` parameter to the `/probe` endpoint:

`$ curl http://localhost:9172/probe?name=failure`

```
script_duration_seconds{script="failure"} 2.008337
script_success{script="failure"} 0
```

A regular expression may be specified with the `pattern` paremeter:

`$ curl http://localhost:9172/probe?pattern=.*`

```
script_duration_seconds{script="failure",nagios_status="CRITICAL",timeout="false",output="CRITICAL; failure"} 0.000973
script_success{script="failure",nagios_status="CRITICAL",timeout="false",output="CRITICAL; failure"} 0
script_exit_code{script="failure",nagios_status="CRITICAL",timeout="false",output="CRITICAL; failure"} 2
script_duration_seconds{script="timeout",nagios_status="CRITICAL",timeout="true",output="Timeout running command"} 2.000661
script_success{script="timeout",nagios_status="CRITICAL",timeout="true",output="Timeout running command"} 0
script_exit_code{script="timeout",nagios_status="CRITICAL",timeout="true",output="Timeout running command"} 2
script_duration_seconds{script="success_silent",nagios_status="OK"} 3.001741
script_success{script="success_silent",nagios_status="OK"} 1
script_exit_code{script="success_silent",nagios_status="OK"} 0
script_duration_seconds{script="success",nagios_status="OK",output="OK; all is OK"} 3.001713
script_success{script="success",nagios_status="OK",output="OK; all is OK"} 1
script_exit_code{script="success",nagios_status="OK",output="OK; all is OK"} 0
```

## Design

YMMV if you're attempting to execute a large number of scripts, and you'd be
better off creating an exporter that can handle your protocol without launching
shell processes for each scrape.
