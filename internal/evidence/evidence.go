// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package evidence collects what can be found about the probable cause of
// an incident, for the follow-up message (internal/followup):
//
//   - what changed in the workload of the service in the last 30 minutes
//     (new version, container restarts, pods not ready, Warning events),
//     from the Kubernetes API (kube.go);
//   - the operations with most failed calls, from the span metrics the
//     error-rate rules use (prom.go);
//   - one error line from the logs, from Loki (loki.go);
//   - a link to failing traces in Grafana Explore with Tempo (traces.go).
//
// Every collector has a short timeout; a failure is logged and the part is
// left out, never fatal. Collect bounds the total time.
package evidence

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/generator"
)

// Defaults of a Collector.
const (
	DefaultWindow  = 30 * time.Minute
	DefaultTimeout = 5 * time.Second
	DefaultTotal   = 12 * time.Second
	// DefaultTempoDatasource is the UID of the Tempo data source of the
	// bundled Grafana (deploy/helm/kamerton/values.yaml).
	DefaultTempoDatasource = "tempo"
)

// Sources are the programs of the system's set of programs.
type Sources struct {
	// Prom is the Prometheus of the set; nil: no metric evidence.
	Prom prometheus.Querier
	// LokiURL is the Loki of the set; empty: no log line.
	LokiURL string
}

// Collector collects evidence. The zero value collects nothing but the
// parts that need no configuration (metrics with Sources.Prom, logs with
// Sources.LokiURL).
type Collector struct {
	// Kube reads pods, ReplicaSets, StatefulSets and events; nil: no
	// workload evidence (no cluster, or the read access is turned off).
	Kube client.Reader
	// GrafanaURL is Grafana's address as people open it; empty: no trace
	// link. TempoDatasource is the UID of its Tempo data source.
	GrafanaURL      string
	TempoDatasource string
	// HTTP is the client for Loki; default: a client with Timeout.
	HTTP *http.Client
	// Window is how far back to look (default 30m), Timeout bounds one
	// collector (default 5s), Total all of them (default 12s).
	Window, Timeout, Total time.Duration
	Logger                 *slog.Logger
	Now                    func() time.Time
}

func (c *Collector) window() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return DefaultWindow
}

func (c *Collector) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Collector) total() time.Duration {
	if c.Total > 0 {
		return c.Total
	}
	return DefaultTotal
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c *Collector) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: c.timeout()}
}

// Collect gathers the evidence about one service of a description. since
// is the start of the incident (the log search starts a little earlier).
// Title and Since of the result are left to the caller.
func (c *Collector) Collect(ctx context.Context, sd *v1alpha1.SystemDescription, svc *v1alpha1.Service, src Sources, since time.Time) generator.RootEvidence {
	ctx, cancel := context.WithTimeout(ctx, c.total())
	defer cancel()
	ev := generator.RootEvidence{Service: svc.Name}
	now := c.now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	run := func(name string, f func(ctx context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, ccancel := context.WithTimeout(ctx, c.timeout())
			defer ccancel()
			if err := f(cctx); err != nil {
				c.log().Warn("evidence collector failed", "collector", name, "system", sd.Name, "service", svc.Name, "error", err)
			}
		}()
	}
	if c.Kube != nil {
		run("kubernetes", func(ctx context.Context) error {
			ch, err := c.Workload(ctx, sd, svc, src.Prom, now)
			mu.Lock()
			ev.Changes = ch
			mu.Unlock()
			return err
		})
	}
	if src.Prom != nil {
		run("prometheus", func(ctx context.Context) error {
			ops, err := c.FailingOperations(ctx, src.Prom, sd, svc, now)
			mu.Lock()
			ev.Operations = ops
			mu.Unlock()
			return err
		})
	}
	if src.LokiURL != "" {
		run("loki", func(ctx context.Context) error {
			line, err := c.LogLine(ctx, src.LokiURL, sd, svc, since, now)
			mu.Lock()
			ev.LogLine = line
			mu.Unlock()
			return err
		})
	}
	ev.TraceURL = c.TraceURL(sd, svc, since, now)
	wg.Wait()
	return ev
}
