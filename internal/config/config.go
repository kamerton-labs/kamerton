// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package config holds the runtime configuration of the core. Every flag can
// also be set with an environment variable KAMERTON_<FLAG_NAME> (dashes become
// underscores), which is how the Helm chart configures the container.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the core configuration.
type Config struct {
	// HTTP server for UI and API.
	ListenAddress string
	// DescriptionFile is an optional description loaded at startup (local mode).
	DescriptionFile string
	// RunbookBaseURL is the external URL of Kamerton (as users open it), used
	// for runbook and alert links in rules and notifications. Empty: no links.
	RunbookBaseURL string

	// Endpoints of the managed (or external) programs. Empty means not configured.
	PrometheusURL    string
	AlertmanagerURL  string
	GrafanaURL       string
	LokiURL          string
	TempoURL         string
	OTelCollectorURL string
	// ExternalPrometheus / ExternalGrafana mark bring-your-own mode.
	ExternalPrometheus bool
	ExternalGrafana    bool
	// The addresses above form the built-in set of programs; more sets are
	// added in the UI and kept in StacksConfigMap (cluster) or StacksFile
	// (local; default: next to the descriptions).
	StacksConfigMap string
	StacksFile      string
	// StackDiscovery lets the UI look for programs running in the cluster
	// (Kubernetes API, read-only).
	StackDiscovery bool

	// Controller (Kubernetes operator) settings.
	EnableController       bool
	MetricsBindAddress     string
	HealthProbeBindAddress string
	LeaderElect            bool
	// WatchNamespace limits the controller to one namespace; empty = all.
	WatchNamespace string
	// Namespace is Kamerton's own namespace (Secret with the bot token,
	// Telegram state). Empty: the service account namespace.
	Namespace string
	// DescriptionName selects the SystemDescription shown and edited by the
	// UI in cluster mode; empty picks one (preferring Kamerton's namespace).
	DescriptionName string

	// Telegram setup.
	TelegramAPIURL     string
	TelegramSecretName string

	// Email setup.
	EmailSecretName string

	// Follow-up messages. AlertmanagerWebhookURL is Kamerton's webhook as
	// Alertmanager reaches it (empty: no webhook in generated routing);
	// WebhookSecretName holds its bearer token. WorkloadEvidence lets the
	// follow-up read pods, ReplicaSets, StatefulSets and events in all
	// namespaces. GrafanaExternalURL is Grafana as people open it, for trace
	// links (empty: no link).
	AlertmanagerWebhookURL string
	WebhookSecretName      string
	WorkloadEvidence       bool
	GrafanaExternalURL     string

	LogLevel string
}

// Register binds flags to c with environment-variable defaults.
func (c *Config) Register(fs *flag.FlagSet) {
	str := func(p *string, name, def, usage string) {
		fs.StringVar(p, name, envString(name, def), usage+envHint(name))
	}
	boolean := func(p *bool, name string, def bool, usage string) {
		fs.BoolVar(p, name, envBool(name, def), usage+envHint(name))
	}
	str(&c.ListenAddress, "listen-address", ":8080", "address of the UI and API server")
	str(&c.DescriptionFile, "description", "", "system description files (comma-separated) or a directory of them (local mode); new systems are created in the directory")
	str(&c.RunbookBaseURL, "external-url", "", "external URL of Kamerton, used for runbook links")
	str(&c.PrometheusURL, "prometheus-url", "", "Prometheus base URL")
	str(&c.AlertmanagerURL, "alertmanager-url", "", "Alertmanager base URL")
	str(&c.GrafanaURL, "grafana-url", "", "Grafana base URL")
	str(&c.LokiURL, "loki-url", "", "Loki base URL")
	str(&c.TempoURL, "tempo-url", "", "Tempo base URL")
	str(&c.OTelCollectorURL, "otel-collector-health-url", "", "OpenTelemetry Collector health_check extension URL")
	boolean(&c.ExternalPrometheus, "external-prometheus", false, "Prometheus is not managed by Kamerton (bring your own)")
	boolean(&c.ExternalGrafana, "external-grafana", false, "Grafana is not managed by Kamerton (bring your own)")
	str(&c.StacksConfigMap, "stacks-configmap", "kamerton-stacks", "ConfigMap (in Kamerton's namespace) holding the sets of programs added in the UI (cluster mode)")
	str(&c.StacksFile, "stacks-file", "", "file holding the sets of programs added in the UI (local mode); empty: .kamerton-stacks.json next to the descriptions, or memory only")
	boolean(&c.StackDiscovery, "stack-discovery", true, "let the UI find programs running in the cluster (lists Services and Prometheus Operator resources; cluster mode)")
	boolean(&c.EnableController, "enable-controller", false, "run the Kubernetes controller (requires cluster access)")
	str(&c.MetricsBindAddress, "metrics-bind-address", ":8081", "controller metrics address (0 disables)")
	str(&c.HealthProbeBindAddress, "health-probe-bind-address", ":8082", "controller health probe address")
	boolean(&c.LeaderElect, "leader-elect", false, "enable leader election for the controller")
	str(&c.WatchNamespace, "watch-namespace", "", "namespace to watch; empty watches all namespaces")
	str(&c.Namespace, "namespace", "", "Kamerton's namespace; empty: the namespace of the service account")
	str(&c.DescriptionName, "description-name", "", "name of the SystemDescription used by the single-description API endpoints in cluster mode; empty picks one")
	str(&c.TelegramAPIURL, "telegram-api-url", "https://api.telegram.org", "Telegram Bot API base URL")
	str(&c.TelegramSecretName, "telegram-secret-name", "kamerton-telegram", "Secret (in Kamerton's namespace) holding the bot token under key bot-token")
	str(&c.EmailSecretName, "email-secret-name", "kamerton-smtp", "Secret (in Kamerton's namespace) holding the SMTP password under key password")
	str(&c.AlertmanagerWebhookURL, "alertmanager-webhook-url", "", "URL of Kamerton's webhook as Alertmanager reaches it, for example http://kamerton.kamerton.svc:8080/api/v1/hooks/alertmanager; empty: no follow-up messages (cluster mode)")
	str(&c.WebhookSecretName, "webhook-secret-name", "kamerton-webhook", "Secret (in Kamerton's namespace) holding the webhook bearer token under key token; created when missing")
	boolean(&c.WorkloadEvidence, "workload-evidence", true, "let follow-up messages read pods, ReplicaSets, StatefulSets and events of the root service (cluster mode)")
	str(&c.GrafanaExternalURL, "grafana-external-url", "", "Grafana as people open it, for links to failing traces in follow-up messages; empty: no link")
	str(&c.LogLevel, "log-level", "info", "log level: debug, info, warn, error")
}

// EnvName returns the environment variable for a flag.
func EnvName(flagName string) string {
	return "KAMERTON_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

func envHint(name string) string { return fmt.Sprintf(" (env %s)", EnvName(name)) }

func envString(name, def string) string {
	if v, ok := os.LookupEnv(EnvName(name)); ok {
		return v
	}
	return def
}

func envBool(name string, def bool) bool {
	if v, ok := os.LookupEnv(EnvName(name)); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
