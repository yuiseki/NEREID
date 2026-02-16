package controller

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMakeJobNameStableAndBounded(t *testing.T) {
	name := makeJobName(strings.Repeat("x", 120))
	if len(name) > 63 {
		t.Fatalf("job name length exceeded: %d", len(name))
	}
	if !strings.HasPrefix(name, "work-") {
		t.Fatalf("job name prefix mismatch: %s", name)
	}
}

func TestPruneArtifactsRemovesEntriesOlderThanRetention(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old-work")
	newPath := filepath.Join(root, "new-work")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-31 * 24 * time.Hour)
	newTime := now.Add(-5 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	c := &Controller{
		cfg: Config{
			ArtifactsHostPath: root,
			ArtifactRetention: 30 * 24 * time.Hour,
		},
		logger:  slog.Default(),
		nowFunc: func() time.Time { return now },
	}

	if err := c.pruneArtifacts(); err != nil {
		t.Fatalf("pruneArtifacts() error = %v", err)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old path should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new path should remain, stat err=%v", err)
	}
}

func TestBuildJobOverpassGeneratesMapLibreArtifactPage(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nereid.yuiseki.net/v1alpha1",
		"kind":       "Work",
		"metadata": map[string]interface{}{
			"name":      "overpass-sample",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "overpassql.map.v1",
			"title": "sample",
			"overpass": map[string]interface{}{
				"endpoint": "https://overpass-api.de/api/interpreter",
				"query":    "[out:json];node(1,2,3,4);out;",
			},
			"render": map[string]interface{}{
				"viewport": map[string]interface{}{
					"center": []interface{}{139.76, 35.68},
					"zoom":   11.0,
				},
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	job, err := c.buildJob(work, "work-overpass-sample", "overpassql.map.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Image; got != overpassJobImage {
		t.Fatalf("unexpected image got=%q want=%q", got, overpassJobImage)
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	for _, needle := range []string{
		"maplibre-gl",
		"osmtogeojson",
		"turf.min.js",
		"fetch(\"./overpass.json\")",
		"map.flyTo(",
		"turf.center(normalized)",
		"node-pins",
		"node-halo",
		"way-emojis",
		"relation-emojis",
		"buildEmojiImage(",
		"classifyConvenienceIcon(",
		"cvs-711",
		"cvs-familymart",
		"cvs-lawson",
		"__icon_image",
		"icon-ignore-placement",
		"circle-stroke-color",
		"raster-saturation",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\nscript:\n%s", needle, script)
		}
	}
}

func TestBuildJobStyleInlineGeneratesPreviewPage(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "style-sample",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "maplibre.style.v1",
			"title": "style sample",
			"style": map[string]interface{}{
				"sourceStyle": map[string]interface{}{
					"mode": "inline",
					"json": `{"version":8,"sources":{"osm":{"type":"raster","tiles":["https://{a,b,c}.tile.openstreetmap.org/{z}/{x}/{y}.png"],"tileSize":256}},"layers":[{"id":"osm","type":"raster","source":"osm"}]}`,
				},
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	job, err := c.buildJob(work, "work-style-sample", "maplibre.style.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Image; got != styleJobImage {
		t.Fatalf("unexpected image got=%q want=%q", got, styleJobImage)
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	for _, needle := range []string{
		"STYLE_MODE=\"inline\"",
		"base64 -d > \"${OUT_DIR}/style.json\"",
		"new maplibregl.Map(",
		"style: \"./style.json\"",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\nscript:\n%s", needle, script)
		}
	}
}

func TestBuildJobDuckdbGeneratesScaffoldPage(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "duckdb-sample",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "duckdb.map.v1",
			"title": "duckdb sample",
			"duckdb": map[string]interface{}{
				"input": map[string]interface{}{
					"uri": "https://example.com/sample.parquet",
				},
				"sql": "select 1;",
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	job, err := c.buildJob(work, "work-duckdb-sample", "duckdb.map.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Image; got != duckdbJobImage {
		t.Fatalf("unexpected image got=%q want=%q", got, duckdbJobImage)
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	for _, needle := range []string{
		"input_uri.txt",
		"query.sql",
		"NEREID duckdb.map.v1 scaffold",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\nscript:\n%s", needle, script)
		}
	}
}

func TestBuildJobGDALRasterGeneratesWorkflowScript(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "gdal-sample",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "gdal.rastertile.v1",
			"title": "gdal sample",
			"raster": map[string]interface{}{
				"input": map[string]interface{}{
					"uri": "https://example.com/sample.tif",
				},
				"nodata": map[string]interface{}{
					"src": "-9999",
					"dst": "0",
				},
				"reprojection": map[string]interface{}{
					"targetEPSG": "EPSG:3857",
					"resampling": "near",
				},
				"tiles": map[string]interface{}{
					"minZoom": int64(2),
					"maxZoom": int64(9),
				},
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	job, err := c.buildJob(work, "work-gdal-sample", "gdal.rastertile.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Image; got != gdalRasterJobImage {
		t.Fatalf("unexpected image got=%q want=%q", got, gdalRasterJobImage)
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	for _, needle := range []string{
		"gdalinfo /tmp/input.tif",
		"gdal_translate -a_nodata",
		"gdalwarp -r",
		"gdal2tiles.py -w none",
		"GeoTIFF inspect -> NoData -> Reproject -> Raster tiles -> Web map",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\nscript:\n%s", needle, script)
		}
	}
}

func TestBuildJobLAZ3DTilesGeneratesWorkflowScript(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "laz-sample",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "laz.3dtiles.v1",
			"title": "laz sample",
			"pointcloud": map[string]interface{}{
				"input": map[string]interface{}{
					"uri": "https://example.com/sample.laz",
				},
				"crs": map[string]interface{}{
					"source":          "EPSG:4326",
					"target":          "EPSG:4978",
					"inAxisOrdering":  "2,1",
					"outAxisOrdering": "1,2",
				},
				"py3dtiles": map[string]interface{}{
					"jobs":           4,
					"pyprojAlwaysXY": true,
				},
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	job, err := c.buildJob(work, "work-laz-sample", "laz.3dtiles.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Image; got != laz3DTilesJobImage {
		t.Fatalf("unexpected image got=%q want=%q", got, laz3DTilesJobImage)
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	for _, needle := range []string{
		"pdal info /tmp/input.laz",
		"filters.reprojection",
		"pdal pipeline /tmp/pdal-pipeline.json",
		"py3dtiles convert /tmp/reprojected.laz",
		"Cesium3DTileset.fromUrl(\"./3dtiles/tileset.json\")",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\nscript:\n%s", needle, script)
		}
	}
}

func TestBuildJobAgentCLIGeneratesCommandWrapperScript(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "agent-cli-sample",
			"namespace": "nereid",
			"annotations": map[string]interface{}{
				userPromptAnnotationKey: "東京都台東区の公園を表示してください。",
			},
		},
		"spec": map[string]interface{}{
			"kind":  "agent.cli.v1",
			"title": "agent sample",
			"agent": map[string]interface{}{
				"image": "node:22-bookworm-slim",
				"command": []interface{}{
					"npx",
					"-y",
					"@google/gemini-cli",
					"--help",
				},
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	job, err := c.buildJob(work, "work-agent-cli-sample", "agent.cli.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Image; got != "node:22-bookworm-slim" {
		t.Fatalf("unexpected image got=%q want=%q", got, "node:22-bookworm-slim")
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	for _, needle := range []string{
		"NEREID_WORK_NAME",
		"NEREID_ARTIFACT_DIR",
		"command.txt",
		"user-input.txt",
		"dialogue.txt",
		"agent.log",
		"https://nereid.yuiseki.net/embed?work=agent-cli-sample",
		"'@google/gemini-cli'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\nscript:\n%s", needle, script)
		}
	}
}

func TestBuildJobAgentCLIRequiresImage(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "agent-cli-invalid",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "agent.cli.v1",
			"title": "agent invalid",
			"agent": map[string]interface{}{
				"script": "echo hello",
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	_, err := c.buildJob(work, "work-agent-cli-invalid", "agent.cli.v1")
	if err == nil {
		t.Fatal("buildJob() expected error for missing image, got nil")
	}
	if !strings.Contains(err.Error(), "spec.agent.image is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildJobUnsupportedKindReturnsError(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "unknown-kind",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "unknown.kind.v1",
			"title": "unknown",
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	_, err := c.buildJob(work, "work-unknown-kind", "unknown.kind.v1")
	if err == nil {
		t.Fatal("buildJob() expected error for unsupported kind, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported spec.kind") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyGrantToJobOverridesQueueRuntimeResourcesAndEnv(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "overpass-sample",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "overpassql.map.v1",
			"title": "sample",
			"overpass": map[string]interface{}{
				"endpoint": "https://overpass-api.de/api/interpreter",
				"query":    "[out:json];node(1,2,3,4);out;",
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			RuntimeClassName:  "gvisor",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
		kube: fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openai",
				Namespace: "nereid",
			},
			Data: map[string][]byte{
				"api-key": []byte("secret-value"),
			},
		}),
	}

	job, err := c.buildJob(work, "work-overpass-sample", "overpassql.map.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}

	grant := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nereid.yuiseki.net/v1alpha1",
		"kind":       "Grant",
		"metadata": map[string]interface{}{
			"name":      "demo-grant",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kueue": map[string]interface{}{
				"localQueueName": "grant-queue",
			},
			"runtimeClassName": "kata",
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"cpu":    "250m",
					"memory": "256Mi",
				},
				"limits": map[string]interface{}{
					"cpu":    "1",
					"memory": "1Gi",
				},
			},
			"env": []interface{}{
				map[string]interface{}{
					"name": "OPENAI_API_KEY",
					"secretKeyRef": map[string]interface{}{
						"name": "openai",
						"key":  "api-key",
					},
				},
			},
		},
	}}

	if err := c.applyGrantToJob(context.Background(), job, grant); err != nil {
		t.Fatalf("applyGrantToJob() error = %v", err)
	}

	if got := job.Labels["kueue.x-k8s.io/queue-name"]; got != "grant-queue" {
		t.Fatalf("queue label mismatch got=%q want=%q", got, "grant-queue")
	}
	if job.Spec.Template.Spec.RuntimeClassName == nil || *job.Spec.Template.Spec.RuntimeClassName != "kata" {
		t.Fatalf("runtimeClassName mismatch got=%v want=%q", job.Spec.Template.Spec.RuntimeClassName, "kata")
	}

	container := job.Spec.Template.Spec.Containers[0]
	cpuReq := container.Resources.Requests[corev1.ResourceCPU]
	if got := cpuReq.String(); got != "250m" {
		t.Fatalf("cpu request mismatch got=%q want=%q", got, "250m")
	}
	memReq := container.Resources.Requests[corev1.ResourceMemory]
	if got := memReq.String(); got != "256Mi" {
		t.Fatalf("memory request mismatch got=%q want=%q", got, "256Mi")
	}
	cpuLim := container.Resources.Limits[corev1.ResourceCPU]
	if got := cpuLim.String(); got != "1" {
		t.Fatalf("cpu limit mismatch got=%q want=%q", got, "1")
	}
	memLim := container.Resources.Limits[corev1.ResourceMemory]
	if got := memLim.String(); got != "1Gi" {
		t.Fatalf("memory limit mismatch got=%q want=%q", got, "1Gi")
	}

	found := false
	for _, ev := range container.Env {
		if ev.Name != "OPENAI_API_KEY" {
			continue
		}
		found = true
		if ev.ValueFrom != nil {
			t.Fatalf("OPENAI_API_KEY should be resolved to a literal value, got=%+v", ev)
		}
		if ev.Value != "secret-value" {
			t.Fatalf("OPENAI_API_KEY value mismatch got=%q want=%q", ev.Value, "secret-value")
		}
	}
	if !found {
		t.Fatal("OPENAI_API_KEY env not injected")
	}
}

func TestAllowedWorksForGrantMaxUsesSelectsEarliestByCreationTimestamp(t *testing.T) {
	makeWork := func(name string, ts time.Time, grant string) *unstructured.Unstructured {
		w := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "nereid.yuiseki.net/v1alpha1",
			"kind":       "Work",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "nereid",
			},
			"spec": map[string]interface{}{
				"kind":  "overpassql.map.v1",
				"title": "x",
			},
		}}
		w.SetCreationTimestamp(metav1.Time{Time: ts})
		if grant != "" {
			_ = unstructured.SetNestedField(w.Object, map[string]interface{}{"name": grant}, "spec", "grantRef")
		}
		return w
	}

	base := time.Date(2026, 2, 15, 7, 0, 0, 0, time.UTC)
	w1 := makeWork("w1", base.Add(1*time.Minute), "g1")
	w2 := makeWork("w2", base.Add(2*time.Minute), "g1")
	w3 := makeWork("w3", base.Add(3*time.Minute), "g1")
	wOther := makeWork("w-other", base.Add(4*time.Minute), "g2")

	all := []*unstructured.Unstructured{w3, wOther, w1, w2}
	allowed := allowedWorkNamesForGrantMaxUses(all, "g1", 2)
	if !allowed["w1"] || !allowed["w2"] {
		t.Fatalf("expected w1/w2 allowed, got=%v", allowed)
	}
	if allowed["w3"] {
		t.Fatalf("expected w3 denied, got=%v", allowed)
	}
	if allowed["w-other"] {
		t.Fatalf("expected w-other ignored, got=%v", allowed)
	}
}

func TestExtractViewportDefaultsAndOverrides(t *testing.T) {
	workDefault := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{},
	}}
	lon, lat, zoom := extractViewport(workDefault)
	if lon != 139.76 || lat != 35.68 || zoom != 11 {
		t.Fatalf("defaults mismatch lon=%f lat=%f zoom=%f", lon, lat, zoom)
	}

	workCustom := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"render": map[string]interface{}{
				"viewport": map[string]interface{}{
					"center": []interface{}{140, "36.1"},
					"zoom":   9,
				},
			},
		},
	}}
	lon, lat, zoom = extractViewport(workCustom)
	if lon != 140 || lat != 36.1 || zoom != 9 {
		t.Fatalf("custom mismatch lon=%f lat=%f zoom=%f", lon, lat, zoom)
	}
}

func TestExtractTileZoomRangeDefaultsAndBounds(t *testing.T) {
	workDefault := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	minZoom, maxZoom := extractTileZoomRange(workDefault)
	if minZoom != 0 || maxZoom != 14 {
		t.Fatalf("defaults mismatch min=%d max=%d", minZoom, maxZoom)
	}

	workCustom := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"raster": map[string]interface{}{
				"tiles": map[string]interface{}{
					"minZoom": -5,
					"maxZoom": 99,
				},
			},
		},
	}}
	minZoom, maxZoom = extractTileZoomRange(workCustom)
	if minZoom != 0 || maxZoom != 24 {
		t.Fatalf("bounds mismatch min=%d max=%d", minZoom, maxZoom)
	}
}
