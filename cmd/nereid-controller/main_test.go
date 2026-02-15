package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildRESTConfigExplicitPath(t *testing.T) {
	cfgPath := writeKubeconfig(t, "https://explicit.example")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "ignored"))

	cfg, err := buildRESTConfig(cfgPath)
	if err != nil {
		t.Fatalf("buildRESTConfig(explicit) error = %v", err)
	}
	if cfg.Host != "https://explicit.example" {
		t.Fatalf("Host mismatch got=%q want=%q", cfg.Host, "https://explicit.example")
	}
}

func TestBuildRESTConfigUsesKubeconfigEnv(t *testing.T) {
	cfgPath := writeKubeconfig(t, "https://env.example")
	t.Setenv("KUBECONFIG", cfgPath)

	cfg, err := buildRESTConfig("")
	if err != nil {
		t.Fatalf("buildRESTConfig(env) error = %v", err)
	}
	if cfg.Host != "https://env.example" {
		t.Fatalf("Host mismatch got=%q want=%q", cfg.Host, "https://env.example")
	}
}

func TestBuildRESTConfigExplicitPathInvalid(t *testing.T) {
	_, err := buildRESTConfig(filepath.Join(t.TempDir(), "missing-config"))
	if err == nil {
		t.Fatal("buildRESTConfig(invalid explicit path) expected error, got nil")
	}
}

func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	content := `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: ` + server + `
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: test-token
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}
