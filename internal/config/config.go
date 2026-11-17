// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package config holds the runtime configuration; every flag is also read from KAMERTON_<FLAG_NAME>.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddress   string
	DescriptionFile string
	// RunbookBaseURL is Kamerton's URL as users open it; empty means no links in rules and notifications.
	RunbookBaseURL string

	// Empty program URLs mean the program is not configured.
	PrometheusURL      string
	AlertmanagerURL    string
	GrafanaURL         string
	LokiURL            string
	TempoURL           string
	OTelCollectorURL   string
	ExternalPrometheus bool
	ExternalGrafana    bool
	// Program sets added in the UI: StacksConfigMap in a cluster, StacksFile locally.
	StacksConfigMap string
	StacksFile      string
	StackDiscovery  bool

	EnableController       bool
	MetricsBindAddress     string
	HealthProbeBindAddress string
	LeaderElect            bool
	// WatchNamespace empty means all namespaces.
	WatchNamespace string
	// Namespace holds the bot token Secret and Telegram state; empty means the service account namespace.
	Namespace string
	// DescriptionName selects the UI's SystemDescription in cluster mode; empty picks one, preferring Kamerton's namespace.
	DescriptionName string

	TelegramAPIURL     string
	TelegramSecretName string

	EmailSecretName string

	// AlertmanagerWebhookURL is the webhook as Alertmanager reaches it, GrafanaExternalURL is Grafana as
	// people open it; empty disables each.
	AlertmanagerWebhookURL string
	WebhookSecretName      string
	WorkloadEvidence       bool
	GrafanaExternalURL     string

	LogLevel string
}

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
