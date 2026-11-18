// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Command kamerton is the Kamerton core: the HTTP server with UI and API, the controller and CLI helpers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kamerton-labs/kamerton/api/v1alpha1"
	"github.com/kamerton-labs/kamerton/internal/clients/alertmanager"
	"github.com/kamerton-labs/kamerton/internal/clients/prometheus"
	"github.com/kamerton-labs/kamerton/internal/config"
	"github.com/kamerton-labs/kamerton/internal/controller"
	"github.com/kamerton-labs/kamerton/internal/deployer"
	"github.com/kamerton-labs/kamerton/internal/description"
	"github.com/kamerton-labs/kamerton/internal/descstore"
	"github.com/kamerton-labs/kamerton/internal/email"
	"github.com/kamerton-labs/kamerton/internal/evidence"
	"github.com/kamerton-labs/kamerton/internal/followup"
	"github.com/kamerton-labs/kamerton/internal/generator"
	"github.com/kamerton-labs/kamerton/internal/gitsync"
	"github.com/kamerton-labs/kamerton/internal/history"
	"github.com/kamerton-labs/kamerton/internal/noise"
	"github.com/kamerton-labs/kamerton/internal/schema"
	"github.com/kamerton-labs/kamerton/internal/server"
	"github.com/kamerton-labs/kamerton/internal/stack"
	"github.com/kamerton-labs/kamerton/internal/tailmute"
	"github.com/kamerton-labs/kamerton/internal/telegram"
	"github.com/kamerton-labs/kamerton/internal/version"
	"github.com/kamerton-labs/kamerton/web"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "validate":
		err = validate(args)
	case "generate":
		err = generate(args)
	case "schema":
		_, err = os.Stdout.Write(schema.JSON())
	case "version":
		fmt.Printf("kamerton %s (%s)\n", version.Version, version.Commit)
	case "help", "-h", "--help":
		fmt.Println("usage: kamerton [serve|validate|generate|schema|version] [flags]")
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func serve(args []string) error {
	// The env file is loaded before the flags are registered: flag defaults
	// come from KAMERTON_* variables. Variables already set win.
	envFile, envFileGiven := config.EnvFileArg(args)
	envSet, err := config.LoadEnvFile(envFile, envFileGiven)
	if err != nil {
		return err
	}
	var cfg config.Config
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfg.Register(fs)
	fs.String("env-file", config.DefaultEnvFile, "file with KAMERTON_* variables (KEY=VALUE lines) loaded at startup; variables already set in the environment win")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.RunbookBaseURL != "" {
		if problem := description.CheckLinkURL(cfg.RunbookBaseURL); problem != "" {
			return fmt.Errorf("--external-url: %s", problem)
		}
	}
	if cfg.GrafanaExternalURL != "" {
		if problem := description.CheckLinkURL(cfg.GrafanaExternalURL); problem != "" {
			return fmt.Errorf("--grafana-external-url: %s", problem)
		}
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)
	if len(envSet) > 0 {
		log.Info("loaded env file", "path", envFile, "variables", len(envSet))
	}
	authCfg, err := config.AuthFromEnv()
	if err != nil {
		return err
	}

	ui, uiBuilt := web.Dist()
	if !uiBuilt {
		log.Warn("web UI is not built; serving a placeholder page (run `make ui`)")
	}
	sets := &stack.Sets{Flags: stackFlags(cfg)}
	opts := server.Options{
		UI: ui, UIBuilt: uiBuilt, Logger: log,
		Generator: generator.Options{RunbookBaseURL: cfg.RunbookBaseURL},
		Sets:      sets,
	}
	if authCfg.Enabled() {
		opts.Auth = server.NewAuth(authCfg.User, authCfg.Password, authCfg.Secret)
	}
	if cfg.PrometheusURL != "" {
		p, err := prometheus.New(cfg.PrometheusURL)
		if err != nil {
			return fmt.Errorf("prometheus client: %w", err)
		}
		opts.Prometheus = p
	}
	if cfg.AlertmanagerURL != "" {
		opts.Alertmanager = alertmanager.New(cfg.AlertmanagerURL)
	}
	var mgr ctrl.Manager
	if cfg.EnableController {
		ns := kamertonNamespace(cfg)
		m, uncached, reconciler, err := newManager(cfg, log, sets, ns)
		if err != nil {
			return err
		}
		mgr = m
		opts.Namespace = ns
		sets.Namespace = ns
		sets.Store = &stack.ConfigMapStateStore{Client: uncached, Namespace: ns, Name: cfg.StacksConfigMap}
		if cfg.StackDiscovery {
			opts.Discoverer = &stack.Discoverer{Client: uncached}
		} else {
			opts.DiscoveryUnavailable = "Discovery is turned off in this installation (Helm: stacks.discovery.enabled=false, flag --stack-discovery=false). Enter the addresses by hand."
		}
		if cfg.GitURL != "" {
			cluster := &descstore.Cluster{Client: uncached, Namespace: ns, WatchNamespace: cfg.WatchNamespace, Name: cfg.DescriptionName}
			every, err := time.ParseDuration(cfg.GitInterval)
			if err != nil || every < 10*time.Second {
				every = gitsync.DefaultInterval
			}
			opts.Git = &gitsync.Service{
				URL: cfg.GitURL, Branch: cfg.GitBranch, Path: cfg.GitPath, Target: cluster, Interval: every,
				Token:  secretValue(uncached, ns, cfg.GitSecretName, "token"),
				Logger: log.With("component", "git"),
			}
			// Leader-only: one replica writes descriptions.
			if err := m.Add(opts.Git); err != nil {
				return err
			}
			log.Info("system descriptions are read from git", "repo", opts.Git.Repo(), "branch", cfg.GitBranch, "path", cfg.GitPath, "every", every.String())
		}
		opts.SecretMissing = func(ctx context.Context, namespace string, ref v1alpha1.SecretKeyRef) bool {
			return controller.SecretMissing(ctx, uncached, namespace, ref)
		}
		opts.Descriptions = &descstore.Cluster{Client: uncached, Namespace: ns, WatchNamespace: cfg.WatchNamespace, Name: cfg.DescriptionName}
		if cfg.DescriptionFile != "" {
			log.Warn("--description is ignored in cluster mode; the description is read from SystemDescription resources", "file", cfg.DescriptionFile)
		}
		identity, _ := os.Hostname()
		tg := &telegram.Service{
			API:          &telegram.Client{BaseURL: cfg.TelegramAPIURL, HTTP: &http.Client{Timeout: 90 * time.Second}},
			Tokens:       &telegram.SecretTokenStore{Client: uncached, Namespace: ns, Name: cfg.TelegramSecretName, Key: telegram.DefaultSecretKey},
			State:        &telegram.ConfigMapStateStore{Client: uncached, Namespace: ns, Name: "kamerton-telegram-state"},
			Descriptions: opts.Descriptions,
			Namespace:    ns,
			Identity:     identity,
			Logger:       log.With("component", "telegram"),
		}
		// The polling loop is a leader-only runnable: with leader election
		// on, only one replica consumes getUpdates.
		if err := mgr.Add(tg); err != nil {
			return err
		}
		opts.Telegram = tg
		opts.Feedback = &noise.Feedback{
			State:        &noise.ConfigMapStateStore{Client: uncached, Namespace: ns, Name: "kamerton-noise-state"},
			Descriptions: opts.Descriptions,
			Receiver:     tg.IsReceiver,
			Logger:       log.With("component", "noise"),
		}
		tg.Callbacks = opts.Feedback
		opts.History = &history.Service{
			Store:        &history.ConfigMapStore{Client: uncached, Namespace: ns, Name: "kamerton-incident-history"},
			Descriptions: opts.Descriptions,
			Receiver:     tg.IsReceiver,
			Logger:       log.With("component", "history"),
		}
		tg.Replies = opts.History
		opts.Email = &email.Service{
			Passwords:    &telegram.SecretTokenStore{Client: uncached, Namespace: ns, Name: cfg.EmailSecretName, Key: email.DefaultSecretKey, Component: "email"},
			State:        &email.ConfigMapStateStore{Client: uncached, Namespace: ns, Name: "kamerton-email-state"},
			Descriptions: opts.Descriptions,
			Sender:       &email.SMTPSender{},
			Namespace:    ns,
			Logger:       log.With("component", "email"),
		}
		if webhook, store, reason := setupWebhook(cfg, log, uncached, ns); webhook != nil {
			// Set before the manager starts: the controller writes the
			// webhook into every AlertmanagerConfig of this namespace.
			opts.Generator.Webhook = webhook
			reconciler.Options.Webhook = webhook
			collector := &evidence.Collector{GrafanaURL: cfg.GrafanaExternalURL, Logger: log.With("component", "evidence")}
			if cfg.WorkloadEvidence {
				collector.Kube = uncached
			}
			opts.FollowUp = &followup.Service{
				Descriptions: opts.Descriptions,
				Collector:    collector,
				State:        &followup.ConfigMapStateStore{Client: uncached, Namespace: ns, Name: "kamerton-followup-state"},
				Telegram:     tg,
				Email:        opts.Email,
				Feedback:     opts.Feedback,
				History:      opts.History,
				Logger:       log.With("component", "followup"),
			}
			opts.History.OnClose = opts.FollowUp.RecordsClosed
			// Leader-only: sweeps open incident records for missed resolves.
			if err := mgr.Add(opts.FollowUp); err != nil {
				return err
			}
			opts.WebhookToken = store
		} else {
			opts.FollowUpUnavailable = reason
		}
	} else {
		opts.TelegramUnavailable = "Telegram setup from the UI is available only in a cluster: the core runs without the Kubernetes controller, so there is no safe place to keep the bot token."
		opts.EmailUnavailable = "Email setup from the UI is available only in a cluster: the core runs without the Kubernetes controller, so there is no safe place to keep the SMTP password."
		if cfg.DescriptionFile != "" {
			store, err := descstore.NewFile(cfg.DescriptionFile)
			if err != nil {
				return err
			}
			list, err := store.List(context.Background())
			if err != nil {
				return err
			}
			for _, snap := range list {
				if snap.SD == nil {
					log.Warn("description has errors; fix it in the UI (YAML mode)", "source", snap.Source, "error", (&descstore.InvalidError{Result: *snap.Invalid}).Error())
					continue
				}
				log.Info("loaded description", "source", snap.Source, "system", snap.ID, "services", len(snap.SD.Spec.Services))
			}
			opts.Descriptions = store
			path := cfg.StacksFile
			if path == "" {
				dir := store.Dir
				if dir == "" && len(store.Paths) > 0 {
					dir = filepath.Dir(store.Paths[0])
				}
				path = filepath.Join(dir, ".kamerton-stacks.json")
			}
			sets.Store = &stack.FileStateStore{Path: path}
			log.Info("sets of programs are kept in a file", "path", path)
		} else if cfg.StacksFile != "" {
			sets.Store = &stack.FileStateStore{Path: cfg.StacksFile}
		}
		opts.FollowUpUnavailable = "Follow-up messages work only in a cluster: Kamerton runs without the Kubernetes controller, so Alertmanager has no webhook to call and there is no Secret for its token. Grouping alerts by cause still works wherever the generated AlertmanagerConfig is applied."
		opts.DiscoveryUnavailable = "Discovery of installed programs works only in a cluster: Kamerton runs without the Kubernetes controller and cannot ask the cluster for its services. Enter the addresses by hand."
	}
	server.LogAuthMode(log, opts.Auth)
	tail, err := parseTailMute(cfg.TailMute)
	if err != nil {
		return err
	}
	if tail > 0 {
		opts.TailMute = &tailmute.Service{Grace: tail, Logger: log.With("component", "tailmute")}
	}
	srv := server.New(opts)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	g, ctx := errgroup.WithContext(ctx)

	// No WriteTimeout: backtests and discovery may take up to a minute; each
	// handler bounds its own work with a context timeout.
	httpSrv := &http.Server{
		Addr: cfg.ListenAddress, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	g.Go(func() error {
		log.Info("serving UI and API", "address", cfg.ListenAddress)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	})
	if t := opts.TailMute; t != nil {
		if mgr != nil {
			// Leader-only, like the other writers.
			if err := mgr.Add(t); err != nil {
				return err
			}
		} else {
			g.Go(func() error { return t.Start(ctx) })
		}
	}
	if mgr != nil {
		g.Go(func() error {
			log.Info("starting controller")
			return mgr.Start(ctx)
		})
	}
	return g.Wait()
}

func stackFlags(cfg config.Config) stack.Flags {
	return stack.Flags{
		URLs: map[string]string{
			stack.KindPrometheus: cfg.PrometheusURL, stack.KindAlertmanager: cfg.AlertmanagerURL,
			stack.KindGrafana: cfg.GrafanaURL, stack.KindLoki: cfg.LokiURL, stack.KindTempo: cfg.TempoURL,
			stack.KindOTelCollector: cfg.OTelCollectorURL,
		},
		External: map[string]bool{
			stack.KindPrometheus: cfg.ExternalPrometheus, stack.KindAlertmanager: cfg.ExternalPrometheus,
			stack.KindGrafana: cfg.ExternalGrafana,
		},
	}
}

func kamertonNamespace(cfg config.Config) string {
	if cfg.Namespace != "" {
		return cfg.Namespace
	}
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(b)); ns != "" {
			return ns
		}
	}
	return "default"
}

// setupWebhook ensures the webhook token Secret exists. On failure it returns nil
// and the reason, so generated routing has no webhook Alertmanager would call in vain.
func setupWebhook(cfg config.Config, log *slog.Logger, c client.Client, ns string) (*generator.WebhookOptions, *telegram.SecretTokenStore, string) {
	if cfg.AlertmanagerWebhookURL == "" {
		return nil, nil, "Follow-up messages are turned off in this installation (Helm: incidents.followUp.enabled=false, flag --alertmanager-webhook-url is empty)."
	}
	if problem := description.CheckLinkURL(cfg.AlertmanagerWebhookURL); problem != "" {
		log.Warn("follow-up messages are off: bad --alertmanager-webhook-url", "problem", problem)
		return nil, nil, "Follow-up messages are off: the webhook URL of this installation is not valid (" + problem + ")."
	}
	store := &telegram.SecretTokenStore{Client: c, Namespace: ns, Name: cfg.WebhookSecretName, Key: followup.TokenKey, Component: "webhook"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tok, err := store.Get(ctx)
	if err == nil && tok == "" {
		if tok, err = followup.NewToken(); err == nil {
			err = store.Set(ctx, tok)
		}
	}
	if err != nil {
		log.Warn("follow-up messages are off: cannot prepare the webhook token Secret", "secret", cfg.WebhookSecretName, "error", err)
		return nil, nil, "Follow-up messages are off: Kamerton cannot read or create its webhook token Secret " + ns + "/" + cfg.WebhookSecretName + " (" + err.Error() + ")."
	}
	log.Info("follow-up messages are on", "webhook", cfg.AlertmanagerWebhookURL, "secret", cfg.WebhookSecretName, "workloadEvidence", cfg.WorkloadEvidence)
	return &generator.WebhookOptions{
		URL:       cfg.AlertmanagerWebhookURL,
		Secret:    v1alpha1.SecretKeyRef{Name: cfg.WebhookSecretName, Key: followup.TokenKey},
		Namespace: ns,
	}, store, ""
}

// newManager also returns an uncached client: a cached one would start
// cluster-wide Secret informers, which RBAC deliberately does not allow.
func newManager(cfg config.Config, log *slog.Logger, sets *stack.Sets, ns string) (ctrl.Manager, client.Client, *controller.SystemDescriptionReconciler, error) {
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, nil, nil, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, nil, err
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("kubernetes config: %w", err)
	}
	mgrOpts := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: cfg.MetricsBindAddress},
		HealthProbeBindAddress: cfg.HealthProbeBindAddress,
		LeaderElection:         cfg.LeaderElect,
		LeaderElectionID:       "kamerton.kamerton.dev",
	}
	if cfg.WatchNamespace != "" {
		mgrOpts.Cache.DefaultNamespaces = map[string]cache.Config{cfg.WatchNamespace: {}}
	}
	// The controller watches one ConfigMap (the sets of programs); the cache
	// holds only that one, not every ConfigMap of the cluster.
	mgrOpts.Cache.ByObject = map[client.Object]cache.ByObject{
		&corev1.ConfigMap{}: {
			Namespaces: map[string]cache.Config{ns: {}},
			Field:      fields.OneTermEqualSelector("metadata.name", cfg.StacksConfigMap),
		},
	}
	mgr, err := ctrl.NewManager(restCfg, mgrOpts)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("controller manager: %w", err)
	}
	uncached, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("kubernetes client: %w", err)
	}
	r := &controller.SystemDescriptionReconciler{
		Client:  mgr.GetClient(),
		Applier: &deployer.KubernetesApplier{Client: mgr.GetClient()},
		Options: generator.Options{RunbookBaseURL: cfg.RunbookBaseURL},
		Sets:    sets, SetsNamespace: ns, SetsConfigMap: cfg.StacksConfigMap,
		Secrets: mgr.GetAPIReader(),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return nil, nil, nil, err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, nil, nil, err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return nil, nil, nil, err
	}
	return mgr, uncached, r, nil
}

func validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	file := fs.String("f", "", "description file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("validate: -f is required")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	res := description.Validate(data)
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s: %s\n", w.Path, w.Message)
	}
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "error: %s: %s\n", e.Path, e.Message)
	}
	if !res.Valid {
		return fmt.Errorf("%s is invalid (%d errors)", *file, len(res.Errors))
	}
	fmt.Printf("%s is valid (%d services)\n", *file, len(res.Document.Spec.Services))
	return nil
}

func generate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	file := fs.String("f", "", "description file")
	outDir := fs.String("o", "", "output directory; prints the rule file to stdout when empty")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("generate: -f is required")
	}
	sd, _, err := description.LoadFile(*file)
	if err != nil {
		return err
	}
	b, err := generator.Generate(sd, generator.Options{})
	if err != nil {
		return err
	}
	if *outDir == "" {
		_, err = os.Stdout.Write(b.RulesYAML)
		return err
	}
	write := func(rel string, data []byte) error {
		p := filepath.Join(*outDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0o644)
	}
	if err := write("rules/"+sd.Name+".rules.yaml", b.RulesYAML); err != nil {
		return err
	}
	for name, d := range b.Dashboards {
		if err := write("dashboards/"+name, d); err != nil {
			return err
		}
	}
	for name, rb := range b.Runbooks {
		if err := write("runbooks/"+name, []byte(rb)); err != nil {
			return err
		}
	}
	if b.AlertmanagerConfig != nil {
		j, err := json.MarshalIndent(b.AlertmanagerConfig, "", "  ")
		if err != nil {
			return err
		}
		if err := write("alertmanager/alertmanagerconfig-spec.json", j); err != nil {
			return err
		}
	}
	fmt.Printf("wrote %d rules, %d dashboards, %d runbooks to %s\n", b.RuleCount, len(b.Dashboards), len(b.Runbooks), *outDir)
	return nil
}

// parseTailMute: an empty value means the default, not a crash. A chart upgraded with
// --reuse-values passes an empty string for a setting the old values did not have.
func parseTailMute(v string) (time.Duration, error) {
	switch strings.TrimSpace(v) {
	case "":
		return tailmute.DefaultGrace, nil
	case "0":
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("--tail-mute: %w", err)
	}
	return d, nil
}

// secretValue reads one key of a Secret on every call; a missing Secret or key is an empty
// value, not an error.
func secretValue(c client.Client, ns, name, key string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var s corev1.Secret
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s)
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return string(s.Data[key]), nil
	}
}
