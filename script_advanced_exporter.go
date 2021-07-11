package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
)

const exporter_name = "script_advanced_exporter"

var (
	showVersion     = flag.Bool("version", false, "Print version information.")
	configFile      = flag.String("config.file", "script_advanced_exporter.yml", "Script exporter configuration file.")
	listenAddress   = flag.String("web.listen-address", ":9172", "The address to listen on for HTTP requests.")
	metricsPath     = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	shell           = flag.String("config.shell", "/bin/sh", "Shell to execute script")
	nagios_statuses = map[int]string{
		0: "OK",
		1: "WARNING",
		2: "CRITICAL",
		4: "DEPENDENT",
	}
)

type Config struct {
	Scripts []*Script `yaml:"scripts"`
}

type Script struct {
	Name    string `yaml:"name"`
	Content string `yaml:"script"`
	Timeout int64  `yaml:"timeout"`
}

type ScriptResult struct {
	ExitCode int
	Output   string
	Timeout  bool
}

type Measurement struct {
	Script       *Script
	ScriptResult *ScriptResult
	Duration     float64
}

const nagios_default_status = "UNKNOWN"

func runScript(script *Script) ScriptResult {
	var stdout_buffer bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(script.Timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, *shell)
	cmd.Stdout = &stdout_buffer

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	go func() {
		<-ctx.Done()
		if ctx.Err() == context.DeadlineExceeded {
			log.Warnf("Killing process %d due timout", cmd.Process.Pid)
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	// Write command to bash's stdin
	bashIn, err := cmd.StdinPipe()

	if err != nil {
		return ScriptResult{2, "Error communicating with process stdin: " + string(err.Error()), false}
	}

	if err = cmd.Start(); err != nil {
		return ScriptResult{2, "Error starting command: " + string(err.Error()), false}
	}

	if _, err = bashIn.Write([]byte(script.Content)); err != nil {
		return ScriptResult{2, "Error communicating with command: " + string(err.Error()), false}
	}

	bashIn.Close()

	// Wait for command stop or timeout
	err = cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		return ScriptResult{2, "Timeout running command", true}
	}

	return ScriptResult{cmd.ProcessState.ExitCode(), stdout_buffer.String(), false}
}

func runScripts(scripts []*Script) []*Measurement {
	measurements := make([]*Measurement, 0)

	ch := make(chan *Measurement)

	for _, script := range scripts {
		go func(script *Script) {
			start := time.Now()
			result := runScript(script)
			duration := time.Since(start).Seconds()

			if result.ExitCode == 0 {
				log.Debugf("OK: %s (after %fs).", script.Name, duration)
			} else {
				log.Infof("ERROR: %s: exit code %d, timeouted: %t, output: %s (failed after %fs).", script.Name, result.ExitCode, result.Timeout, result.Output, duration)
			}

			ch <- &Measurement{
				Script:       script,
				Duration:     duration,
				ScriptResult: &result,
			}
		}(script)
	}

	for i := 0; i < len(scripts); i++ {
		measurements = append(measurements, <-ch)
	}

	return measurements
}

func scriptFilter(scripts []*Script, name, pattern string) (filteredScripts []*Script, err error) {
	if name == "" && pattern == "" {
		err = errors.New("`name` or `pattern` required")
		return
	}

	var patternRegexp *regexp.Regexp

	if pattern != "" {
		patternRegexp, err = regexp.Compile(pattern)

		if err != nil {
			return
		}
	}

	for _, script := range scripts {
		if script.Name == name || (pattern != "" && patternRegexp.MatchString(script.Name)) {
			filteredScripts = append(filteredScripts, script)
		}
	}

	return
}

func formatStdOut(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

func scriptRunHandler(w http.ResponseWriter, r *http.Request, config *Config) {
	params := r.URL.Query()
	name := params.Get("name")
	pattern := params.Get("pattern")

	scripts, err := scriptFilter(config.Scripts, name, pattern)

	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	measurements := runScripts(scripts)

	for _, measurement := range measurements {
		// prepare status
		var status int

		if measurement.ScriptResult.ExitCode == 0 {
			status = 1
		} else {
			status = 0
		}

		// prepare labels
		nagios_status, found := nagios_statuses[measurement.ScriptResult.ExitCode]
		if !found {
			nagios_status = nagios_default_status
		}
		var labels strings.Builder
		fmt.Fprintf(&labels, "script=\"%s\",nagios_status=\"%s\"",
			measurement.Script.Name,
			nagios_status,
		)

		stdout := formatStdOut(measurement.ScriptResult.Output)
		if len(stdout) > 0 {
			fmt.Fprintf(&labels, ",output=\"%s\"", stdout)
		}
		labels_str := labels.String()

		// print metrics
		fmt.Fprintf(w, "script_duration_seconds{%s} %f\n", labels_str, measurement.Duration)
		fmt.Fprintf(w, "script_success{%s} %d\n", labels_str, status)
		fmt.Fprintf(w, "script_exit_code{%s} %d\n", labels_str, measurement.ScriptResult.ExitCode)
	}
}

func init() {
	prometheus.MustRegister(version.NewCollector(exporter_name))
}

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Print(exporter_name))
		os.Exit(0)
	}

	log.Infof("Starting %s %s", exporter_name, version.Info())

	yamlFile, err := ioutil.ReadFile(*configFile)

	if err != nil {
		log.Fatalf("Error reading config file: %s", err)
	}

	config := Config{}

	err = yaml.Unmarshal(yamlFile, &config)

	if err != nil {
		log.Fatalf("Error parsing config file: %s", err)
	}

	log.Infof("Loaded %d script configurations", len(config.Scripts))

	for _, script := range config.Scripts {
		if script.Timeout == 0 {
			script.Timeout = 15
		}
	}

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		scriptRunHandler(w, r, &config)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
			<head><title>Script Exporter</title></head>
			<body>
			<h1>Script Exporter</h1>
			<p><a href="` + *metricsPath + `">Metrics</a></p>
			</body>
			</html>`))
	})

	log.Infoln("Listening on", *listenAddress)

	if err := http.ListenAndServe(*listenAddress, nil); err != nil {
		log.Fatalf("Error starting HTTP server: %s", err)
	}
}
