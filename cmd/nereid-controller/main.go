package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yuiseki/NEREID/internal/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	cfg := controller.Config{}
	var resync time.Duration
	var kubeconfig string

	flag.StringVar(&cfg.WorkNamespace, "work-namespace", "nereid", "Namespace containing Work resources. Use empty string for all namespaces.")
	flag.StringVar(&cfg.JobNamespace, "job-namespace", "nereid-work", "Namespace where Jobs are created.")
	flag.StringVar(&cfg.LocalQueueName, "local-queue-name", "nereid-localq", "Kueue LocalQueue name added to Job labels.")
	flag.StringVar(&cfg.RuntimeClassName, "runtime-class-name", "gvisor", "runtimeClassName for Job Pods.")
	flag.StringVar(&cfg.ArtifactsHostPath, "artifacts-host-path", "/var/lib/nereid/artifacts", "Host path mounted for artifacts.")
	flag.StringVar(&cfg.ArtifactBaseURL, "artifact-base-url", "http://nereid-artifacts.yuiseki.com", "Base URL used for Work.status.artifactUrl.")
	flag.DurationVar(&cfg.ArtifactRetention, "artifact-retention", 30*24*time.Hour, "Retention window for entries under artifacts-host-path.")
	flag.DurationVar(&resync, "resync-interval", 5*time.Second, "Reconcile interval.")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (for local execution).")
	flag.Parse()

	if cfg.WorkNamespace == metav1.NamespaceAll {
		cfg.WorkNamespace = ""
	}
	cfg.ResyncInterval = resync

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	restCfg, err := buildRESTConfig(kubeconfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("build kubernetes config: %w", err))
		os.Exit(1)
	}

	dc, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("create dynamic client: %w", err))
		os.Exit(1)
	}
	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("create typed client: %w", err))
		os.Exit(1)
	}

	ctrl := controller.New(dc, kc, cfg, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := ctrl.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func buildRESTConfig(explicitPath string) (*rest.Config, error) {
	if explicitPath != "" {
		return clientcmd.BuildConfigFromFlags("", explicitPath)
	}

	if envPath := os.Getenv("KUBECONFIG"); envPath != "" {
		return clientcmd.BuildConfigFromFlags("", envPath)
	}

	inCluster, err := rest.InClusterConfig()
	if err == nil {
		return inCluster, nil
	}

	if home := homedir.HomeDir(); home != "" {
		path := filepath.Join(home, ".kube", "config")
		if _, statErr := os.Stat(path); statErr == nil {
			return clientcmd.BuildConfigFromFlags("", path)
		}
	}

	return nil, fmt.Errorf("no usable kubeconfig found: %w", err)
}
