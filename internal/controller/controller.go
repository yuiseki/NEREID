package controller

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

var workGVR = schema.GroupVersionResource{
	Group:    "nereid.yuiseki.net",
	Version:  "v1alpha1",
	Resource: "works",
}

var grantGVR = schema.GroupVersionResource{
	Group:    "nereid.yuiseki.net",
	Version:  "v1alpha1",
	Resource: "grants",
}

const (
	overpassJobImage   = "curlimages/curl:8.5.0"
	styleJobImage      = "curlimages/curl:8.5.0"
	duckdbJobImage     = "curlimages/curl:8.5.0"
	gdalRasterJobImage = "osgeo/gdal:ubuntu-small-latest"
	laz3DTilesJobImage = "pdal/pdal:2.7"
)

type Config struct {
	WorkNamespace     string
	JobNamespace      string
	LocalQueueName    string
	RuntimeClassName  string
	ArtifactsHostPath string
	ArtifactBaseURL   string
	ArtifactRetention time.Duration
	ResyncInterval    time.Duration
}

type Controller struct {
	dynamic dynamic.Interface
	kube    kubernetes.Interface
	cfg     Config
	logger  *slog.Logger
	nowFunc func() time.Time
}

func New(dynamicClient dynamic.Interface, kubeClient kubernetes.Interface, cfg Config, logger *slog.Logger) *Controller {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ArtifactRetention <= 0 {
		cfg.ArtifactRetention = 30 * 24 * time.Hour
	}
	return &Controller{
		dynamic: dynamicClient,
		kube:    kubeClient,
		cfg:     cfg,
		logger:  logger,
		nowFunc: time.Now,
	}
}

func (c *Controller) Run(ctx context.Context) error {
	c.logger.Info("controller started",
		"workNamespace", c.cfg.WorkNamespace,
		"jobNamespace", c.cfg.JobNamespace,
		"localQueueName", c.cfg.LocalQueueName,
	)

	if err := c.reconcileAll(ctx); err != nil {
		c.logger.Error("initial reconcile failed", "error", err)
	}

	ticker := time.NewTicker(c.cfg.ResyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("controller stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := c.reconcileAll(ctx); err != nil {
				c.logger.Error("reconcile loop failed", "error", err)
			}
		}
	}
}

func (c *Controller) reconcileAll(ctx context.Context) error {
	if err := c.pruneArtifacts(); err != nil {
		c.logger.Error("artifact prune failed", "error", err)
	}

	ns := c.cfg.WorkNamespace
	if ns == "" {
		ns = metav1.NamespaceAll
	}

	list, err := c.dynamic.Resource(workGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list works: %w", err)
	}

	for i := range list.Items {
		work := &list.Items[i]
		if err := c.reconcileWork(ctx, work); err != nil {
			c.logger.Error("reconcile work failed",
				"work", work.GetName(),
				"namespace", work.GetNamespace(),
				"error", err,
			)
		}
	}
	return nil
}

func (c *Controller) reconcileWork(ctx context.Context, work *unstructured.Unstructured) error {
	kind, _, err := unstructured.NestedString(work.Object, "spec", "kind")
	if err != nil {
		return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to read spec.kind: %v", err), "")
	}

	grantName, _, err := unstructured.NestedString(work.Object, "spec", "grantRef", "name")
	if err != nil {
		return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to read spec.grantRef.name: %v", err), "")
	}
	grantName = strings.TrimSpace(grantName)

	var grant *unstructured.Unstructured
	if grantName != "" {
		obj, getErr := c.dynamic.Resource(grantGVR).Namespace(work.GetNamespace()).Get(ctx, grantName, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("grant %q not found", grantName), "")
		}
		if getErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to get grant %q: %v", grantName, getErr), "")
		}
		grant = obj
		if validateErr := c.validateGrantForWork(ctx, work, kind, grant); validateErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", validateErr.Error(), "")
		}
	}

	jobName := makeJobName(work.GetName())
	job, err := c.kube.BatchV1().Jobs(c.cfg.JobNamespace).Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		newJob, buildErr := c.buildJob(work, jobName, kind)
		if buildErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", buildErr.Error(), "")
		}
		if grant != nil {
			if applyErr := c.applyGrantToJob(newJob, grant); applyErr != nil {
				return c.updateWorkStatus(ctx, work, "Error", applyErr.Error(), "")
			}
		}
		if _, createErr := c.kube.BatchV1().Jobs(c.cfg.JobNamespace).Create(ctx, newJob, metav1.CreateOptions{}); createErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to create job: %v", createErr), "")
		}
		c.logger.Info("created job for work",
			"work", work.GetName(),
			"workNamespace", work.GetNamespace(),
			"job", jobName,
			"jobNamespace", c.cfg.JobNamespace,
		)
		return c.updateWorkStatus(ctx, work, "Submitted", "job created", artifactURL(c.cfg.ArtifactBaseURL, work.GetName()))
	}
	if err != nil {
		return fmt.Errorf("get job %s/%s: %w", c.cfg.JobNamespace, jobName, err)
	}

	phase, message := phaseFromJob(job)
	url := artifactURL(c.cfg.ArtifactBaseURL, work.GetName())
	return c.updateWorkStatus(ctx, work, phase, message, url)
}

func (c *Controller) buildJob(work *unstructured.Unstructured, jobName, kind string) (*batchv1.Job, error) {
	switch kind {
	case "overpassql.map.v1":
		endpoint, _, err := unstructured.NestedString(work.Object, "spec", "overpass", "endpoint")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.overpass.endpoint: %v", err)
		}
		query, _, err := unstructured.NestedString(work.Object, "spec", "overpass", "query")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.overpass.query: %v", err)
		}
		if endpoint == "" || query == "" {
			return nil, fmt.Errorf("spec.overpass.endpoint and spec.overpass.query are required")
		}
		lon, lat, zoom := extractViewport(work)
		script := buildOverpassScript(work.GetName(), endpoint, query, lon, lat, zoom)
		return c.buildScriptJob(work, jobName, overpassJobImage, script), nil

	case "maplibre.style.v1":
		styleMode, _, err := unstructured.NestedString(work.Object, "spec", "style", "sourceStyle", "mode")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.style.sourceStyle.mode: %v", err)
		}
		styleJSON, _, err := unstructured.NestedString(work.Object, "spec", "style", "sourceStyle", "json")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.style.sourceStyle.json: %v", err)
		}
		styleURL, _, err := unstructured.NestedString(work.Object, "spec", "style", "sourceStyle", "url")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.style.sourceStyle.url: %v", err)
		}
		if styleMode == "" {
			styleMode = "inline"
		}
		if styleMode == "inline" && styleJSON == "" {
			return nil, fmt.Errorf("spec.style.sourceStyle.json is required when mode=inline")
		}
		if styleMode == "url" && styleURL == "" {
			return nil, fmt.Errorf("spec.style.sourceStyle.url is required when mode=url")
		}
		if styleMode != "inline" && styleMode != "url" {
			return nil, fmt.Errorf("unsupported spec.style.sourceStyle.mode=%q", styleMode)
		}
		lon, lat, zoom := extractViewport(work)
		script := buildStyleScript(work.GetName(), styleMode, styleJSON, styleURL, lon, lat, zoom)
		return c.buildScriptJob(work, jobName, styleJobImage, script), nil

	case "duckdb.map.v1":
		inputURI, _, err := unstructured.NestedString(work.Object, "spec", "duckdb", "input", "uri")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.duckdb.input.uri: %v", err)
		}
		sql, _, err := unstructured.NestedString(work.Object, "spec", "duckdb", "sql")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.duckdb.sql: %v", err)
		}
		if inputURI == "" || sql == "" {
			return nil, fmt.Errorf("spec.duckdb.input.uri and spec.duckdb.sql are required")
		}
		lon, lat, zoom := extractViewport(work)
		script := buildDuckdbScript(work.GetName(), inputURI, sql, lon, lat, zoom)
		return c.buildScriptJob(work, jobName, duckdbJobImage, script), nil

	case "gdal.rastertile.v1":
		inputURI, _, err := nestedStringAny(work.Object, "spec", "raster", "input", "uri")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.raster.input.uri: %v", err)
		}
		if strings.TrimSpace(inputURI) == "" {
			return nil, fmt.Errorf("spec.raster.input.uri is required")
		}

		srcNoData, _, err := nestedStringAny(work.Object, "spec", "raster", "nodata", "src")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.raster.nodata.src: %v", err)
		}
		dstNoData, _, err := nestedStringAny(work.Object, "spec", "raster", "nodata", "dst")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.raster.nodata.dst: %v", err)
		}
		targetSRS, _, err := nestedStringAny(work.Object, "spec", "raster", "reprojection", "targetSRS")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.raster.reprojection.targetSRS: %v", err)
		}
		if strings.TrimSpace(targetSRS) == "" {
			targetSRS, _, err = nestedStringAny(work.Object, "spec", "raster", "reprojection", "targetEPSG")
			if err != nil {
				return nil, fmt.Errorf("failed to read spec.raster.reprojection.targetEPSG: %v", err)
			}
		}
		if strings.TrimSpace(targetSRS) == "" {
			targetSRS = "EPSG:3857"
		}
		resampling, _, err := nestedStringAny(work.Object, "spec", "raster", "reprojection", "resampling")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.raster.reprojection.resampling: %v", err)
		}
		if strings.TrimSpace(resampling) == "" {
			resampling = "near"
		}
		minZoom, maxZoom := extractTileZoomRange(work)
		lon, lat, zoom := extractViewport(work)
		script := buildGDALRasterScript(work.GetName(), inputURI, srcNoData, dstNoData, targetSRS, resampling, minZoom, maxZoom, lon, lat, zoom)
		return c.buildScriptJob(work, jobName, gdalRasterJobImage, script), nil

	case "laz.3dtiles.v1":
		inputURI, _, err := nestedStringAny(work.Object, "spec", "pointcloud", "input", "uri")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.pointcloud.input.uri: %v", err)
		}
		if strings.TrimSpace(inputURI) == "" {
			return nil, fmt.Errorf("spec.pointcloud.input.uri is required")
		}

		sourceSRS, _, err := nestedStringAny(work.Object, "spec", "pointcloud", "crs", "source")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.pointcloud.crs.source: %v", err)
		}
		if strings.TrimSpace(sourceSRS) == "" {
			return nil, fmt.Errorf("spec.pointcloud.crs.source is required")
		}
		targetSRS, _, err := nestedStringAny(work.Object, "spec", "pointcloud", "crs", "target")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.pointcloud.crs.target: %v", err)
		}
		if strings.TrimSpace(targetSRS) == "" {
			targetSRS = sourceSRS
		}
		inAxisOrdering, _, err := nestedStringAny(work.Object, "spec", "pointcloud", "crs", "inAxisOrdering")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.pointcloud.crs.inAxisOrdering: %v", err)
		}
		outAxisOrdering, _, err := nestedStringAny(work.Object, "spec", "pointcloud", "crs", "outAxisOrdering")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.pointcloud.crs.outAxisOrdering: %v", err)
		}
		pyprojAlwaysXY, _, err := unstructured.NestedBool(work.Object, "spec", "pointcloud", "py3dtiles", "pyprojAlwaysXY")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.pointcloud.py3dtiles.pyprojAlwaysXY: %v", err)
		}
		py3dtilesJobs := extractPointcloudJobs(work)
		lon, lat, zoom := extractViewport(work)
		script := buildLAZ3DTilesScript(work.GetName(), inputURI, sourceSRS, targetSRS, inAxisOrdering, outAxisOrdering, pyprojAlwaysXY, py3dtilesJobs, lon, lat, zoom)
		return c.buildScriptJob(work, jobName, laz3DTilesJobImage, script), nil

	default:
		return nil, fmt.Errorf("unsupported spec.kind=%q", kind)
	}
}

func (c *Controller) buildScriptJob(work *unstructured.Unstructured, jobName, image, script string) *batchv1.Job {
	suspend := true
	hostPathType := corev1.HostPathDirectory
	workName := work.GetName()
	workNamespace := work.GetNamespace()
	deadlineSeconds := extractDeadlineSeconds(work)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: c.cfg.JobNamespace,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": c.cfg.LocalQueueName,
				"nereid.yuiseki.net/work":   workName,
			},
			Annotations: map[string]string{
				"nereid.yuiseki.net/work-name":      workName,
				"nereid.yuiseki.net/work-namespace": workNamespace,
			},
		},
		Spec: batchv1.JobSpec{
			Suspend:               &suspend,
			BackoffLimit:          int32Ptr(0),
			ActiveDeadlineSeconds: &deadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"nereid.yuiseki.net/work": workName,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "task",
							Image:   image,
							Command: []string{"sh", "-lc"},
							Args:    []string{script},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    mustParseQuantity("100m"),
									corev1.ResourceMemory: mustParseQuantity("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    mustParseQuantity("500m"),
									corev1.ResourceMemory: mustParseQuantity("512Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "artifacts",
									MountPath: "/artifacts",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "artifacts",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: c.cfg.ArtifactsHostPath,
									Type: &hostPathType,
								},
							},
						},
					},
				},
			},
		},
	}

	if c.cfg.RuntimeClassName != "" {
		job.Spec.Template.Spec.RuntimeClassName = &c.cfg.RuntimeClassName
	}

	return job
}

func buildOverpassScript(workName, endpoint, query string, centerLon, centerLat, zoom float64) string {
	queryB64 := base64.StdEncoding.EncodeToString([]byte(query))
	return fmt.Sprintf(`set -euo pipefail
WORK=%q
OUT_DIR="/artifacts/${WORK}"
mkdir -p "${OUT_DIR}"

ENDPOINT=%q
QUERY_B64=%q

printf '%%s' "${QUERY_B64}" | base64 -d > /tmp/overpass.ql

echo "fetching overpass..."
curl -fL --retry 3 --retry-delay 2 --connect-timeout 20 --max-time 240 -sS -X POST --data-urlencode data@/tmp/overpass.ql "${ENDPOINT}" > "${OUT_DIR}/overpass.json"

cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID artifact</title>
    <link href="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.css" rel="stylesheet" />
    <style>
      html, body { margin: 0; height: 100%%; font-family: sans-serif; }
      #map { position: absolute; inset: 0; }
      #panel {
        position: absolute; z-index: 1; top: 12px; left: 12px;
        background: rgba(255,255,255,0.92); padding: 8px 10px; border-radius: 6px;
        font-size: 12px; max-width: min(320px, calc(100vw - 40px));
      }
    </style>
  </head>
  <body>
    <div id="panel">
      <strong>NEREID artifact</strong><br/>
      Overpass JSON -> GeoJSON -> MapLibre<br/>
      <a href="./overpass.json">overpass.json</a>
      <div id="stats"></div>
    </div>
    <div id="map"></div>

    <script src="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.js"></script>
    <script src="https://unpkg.com/osmtogeojson@3.0.0-beta.5/osmtogeojson.js"></script>
    <script src="https://unpkg.com/@turf/turf@7.2.0/turf.min.js"></script>
    <script>
      function inferOsmType(feature) {
        const p = feature.properties || {};
        if (typeof p.type === "string" && p.type) return p.type;
        if (typeof p["@id"] === "string" && p["@id"].includes("/")) return p["@id"].split("/")[0];
        if (typeof feature.id === "string" && feature.id.includes("/")) return feature.id.split("/")[0];
        return "";
      }

      function isClosedRing(line) {
        if (!Array.isArray(line) || line.length < 4) return false;
        const a = line[0];
        const b = line[line.length - 1];
        return Array.isArray(a) && Array.isArray(b) && a[0] === b[0] && a[1] === b[1];
      }

      function normalizeGeoJSON(input) {
        const out = [];
        for (const f of input.features || []) {
          if (!f || !f.geometry) continue;
          const osmType = inferOsmType(f);
          const props = Object.assign({}, f.properties || {}, { __osm_type: osmType });
          const g = f.geometry;

          if (osmType === "way" && g.type === "LineString" && isClosedRing(g.coordinates || [])) {
            out.push({
              type: "Feature",
              properties: props,
              geometry: { type: "Polygon", coordinates: [g.coordinates] }
            });
            continue;
          }

          out.push({ type: "Feature", properties: props, geometry: g });
        }
        return { type: "FeatureCollection", features: out };
      }

      function buildPinImage(size) {
        const canvas = document.createElement("canvas");
        canvas.width = size;
        canvas.height = size;
        const ctx = canvas.getContext("2d");
        const cx = size / 2;
        const topY = size * 0.18;
        const bottomY = size - 2;
        const rx = size * 0.22;

        ctx.fillStyle = "#e53935";
        ctx.strokeStyle = "#ffffff";
        ctx.lineWidth = 2;
        ctx.beginPath();
        ctx.moveTo(cx, bottomY);
        ctx.bezierCurveTo(cx + rx * 1.4, size * 0.68, cx + rx * 1.35, size * 0.4, cx, topY);
        ctx.bezierCurveTo(cx - rx * 1.35, size * 0.4, cx - rx * 1.4, size * 0.68, cx, bottomY);
        ctx.closePath();
        ctx.fill();
        ctx.stroke();

        ctx.fillStyle = "#ffffff";
        ctx.beginPath();
        ctx.arc(cx, size * 0.38, rx * 0.45, 0, Math.PI * 2);
        ctx.fill();

        return ctx.getImageData(0, 0, size, size);
      }

      function buildEmojiImage(emoji, size, bgColor) {
        const canvas = document.createElement("canvas");
        canvas.width = size;
        canvas.height = size;
        const ctx = canvas.getContext("2d");
        const cx = size / 2;
        const cy = size / 2;
        const r = size * 0.44;

        ctx.fillStyle = bgColor;
        ctx.beginPath();
        ctx.arc(cx, cy, r, 0, Math.PI * 2);
        ctx.fill();

        ctx.strokeStyle = "rgba(255,255,255,0.9)";
        ctx.lineWidth = Math.max(2, size * 0.05);
        ctx.beginPath();
        ctx.arc(cx, cy, r, 0, Math.PI * 2);
        ctx.stroke();

        ctx.font = Math.floor(size * 0.52) + "px 'Apple Color Emoji','Segoe UI Emoji','Noto Color Emoji',sans-serif";
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        ctx.fillText(emoji, cx, cy + size * 0.02);

        return ctx.getImageData(0, 0, size, size);
      }

      function buildStoreBadgeImage(label, size, topBand, bottomBand, textColor) {
        const canvas = document.createElement("canvas");
        canvas.width = size;
        canvas.height = size;
        const ctx = canvas.getContext("2d");
        const w = size * 0.76;
        const h = size * 0.62;
        const x = (size - w) / 2;
        const y = size * 0.2;
        const r = size * 0.14;

        ctx.fillStyle = "#ffffff";
        ctx.strokeStyle = "rgba(15,23,42,0.35)";
        ctx.lineWidth = 2;
        ctx.beginPath();
        ctx.moveTo(x+r, y);
        ctx.arcTo(x+w, y, x+w, y+h, r);
        ctx.arcTo(x+w, y+h, x, y+h, r);
        ctx.arcTo(x, y+h, x, y, r);
        ctx.arcTo(x, y, x+w, y, r);
        ctx.closePath();
        ctx.fill();
        ctx.stroke();

        ctx.fillStyle = topBand;
        ctx.fillRect(x + 1, y + 1, w - 2, h * 0.2);
        ctx.fillStyle = bottomBand;
        ctx.fillRect(x + 1, y + h * 0.8 - 1, w - 2, h * 0.2);

        ctx.fillStyle = textColor;
        ctx.font = Math.floor(size * 0.25) + "px sans-serif";
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        ctx.fillText(label, size / 2, y + h * 0.52);

        return ctx.getImageData(0, 0, size, size);
      }

      function normalizeStoreText(feature) {
        const p = feature.properties || {};
        return [
          p.brand, p["brand:en"], p["brand:ja"],
          p.name, p["name:en"], p["name:ja"],
          p.operator, p["operator:en"], p["operator:ja"],
          p.chain
        ].filter(Boolean).join(" ").toLowerCase();
      }

      function classifyConvenienceIcon(feature) {
        const text = normalizeStoreText(feature);
        if (!text) return "node-pin";
        if (text.includes("7-eleven") || text.includes("7 eleven") || text.includes("seven-eleven") || text.includes("セブン")) {
          return "cvs-711";
        }
        if (text.includes("familymart") || text.includes("family mart") || text.includes("ファミリーマート")) {
          return "cvs-familymart";
        }
        if (text.includes("lawson") || text.includes("ローソン")) {
          return "cvs-lawson";
        }
        return "node-pin";
      }

      function toPointFeatures(features) {
        const out = [];
        for (const f of features) {
          try {
            const p = turf.pointOnFeature(f);
            if (p && p.geometry && p.geometry.type === "Point") {
              out.push({
                type: "Feature",
                properties: Object.assign({}, f.properties || {}),
                geometry: p.geometry
              });
            }
          } catch (_) {}
        }
        return out;
      }

      (async function main() {
        const map = new maplibregl.Map({
          container: "map",
          style: {
            version: 8,
            sources: {
              osm: {
                type: "raster",
                tiles: [
                  "https://a.tile.openstreetmap.org/{z}/{x}/{y}.png",
                  "https://b.tile.openstreetmap.org/{z}/{x}/{y}.png",
                  "https://c.tile.openstreetmap.org/{z}/{x}/{y}.png"
                ],
                tileSize: 256
              }
            },
            layers: [{ id: "osm", type: "raster", source: "osm" }]
          },
          center: [%f, %f],
          zoom: %f
        });

        const overpass = await fetch("./overpass.json").then((r) => r.json());
        const normalized = normalizeGeoJSON(osmtogeojson(overpass));

        const fillFeatures = normalized.features.filter((f) => {
          const t = f.properties && f.properties.__osm_type;
          const g = f.geometry && f.geometry.type;
          const isArea = g === "Polygon" || g === "MultiPolygon";
          return (t === "relation" || t === "way") && isArea;
        });
        const relationAreaFeatures = normalized.features.filter((f) => {
          const t = f.properties && f.properties.__osm_type;
          const g = f.geometry && f.geometry.type;
          return t === "relation" && (g === "Polygon" || g === "MultiPolygon");
        });
        const wayGeometryFeatures = normalized.features.filter((f) => {
          const t = f.properties && f.properties.__osm_type;
          const g = f.geometry && f.geometry.type;
          const isWayGeom = g === "Polygon" || g === "MultiPolygon" || g === "LineString" || g === "MultiLineString";
          return t === "way" && isWayGeom;
        });
        const relationEmojiPoints = toPointFeatures(relationAreaFeatures);
        const wayEmojiPoints = toPointFeatures(wayGeometryFeatures);
        const nodeFeatures = normalized.features.filter((f) => {
          const t = f.properties && f.properties.__osm_type;
          const g = f.geometry && f.geometry.type;
          return t === "node" && g === "Point";
        });
        const convenienceNodeFeatures = nodeFeatures.map((f) => {
          const icon = classifyConvenienceIcon(f);
          return {
            type: "Feature",
            geometry: f.geometry,
            properties: Object.assign({}, f.properties || {}, { __icon_image: icon })
          };
        });
        const iconCounts = { "cvs-711": 0, "cvs-familymart": 0, "cvs-lawson": 0, "node-pin": 0 };
        for (const f of convenienceNodeFeatures) {
          const icon = (f.properties && f.properties.__icon_image) || "node-pin";
          if (Object.prototype.hasOwnProperty.call(iconCounts, icon)) {
            iconCounts[icon] += 1;
          }
        }

        map.on("load", () => {
          map.addImage("node-pin", buildPinImage(40), { pixelRatio: 2 });
          map.addImage("cvs-711", buildStoreBadgeImage("7", 46, "#f97316", "#16a34a", "#dc2626"), { pixelRatio: 2 });
          map.addImage("cvs-familymart", buildStoreBadgeImage("FM", 46, "#2563eb", "#10b981", "#1d4ed8"), { pixelRatio: 2 });
          map.addImage("cvs-lawson", buildStoreBadgeImage("L", 46, "#3b82f6", "#2563eb", "#1e3a8a"), { pixelRatio: 2 });
          map.addImage("way-emoji", buildEmojiImage("🛣️", 44, "rgba(43,108,176,0.82)"), { pixelRatio: 2 });
          map.addImage("relation-emoji", buildEmojiImage("🧩", 44, "rgba(123,63,228,0.82)"), { pixelRatio: 2 });

          map.addSource("areas", { type: "geojson", data: { type: "FeatureCollection", features: fillFeatures } });
          map.addSource("nodes", { type: "geojson", data: { type: "FeatureCollection", features: convenienceNodeFeatures } });
          map.addSource("way-emoji-points", { type: "geojson", data: { type: "FeatureCollection", features: wayEmojiPoints } });
          map.addSource("relation-emoji-points", { type: "geojson", data: { type: "FeatureCollection", features: relationEmojiPoints } });

          map.addLayer({
            id: "area-fill",
            type: "fill",
            source: "areas",
            paint: { "fill-color": "#1f77b4", "fill-opacity": 0.25 }
          });
          map.addLayer({
            id: "area-outline",
            type: "line",
            source: "areas",
            paint: { "line-color": "#1f77b4", "line-width": 1.5 }
          });
          map.addLayer({
            id: "node-pins",
            type: "symbol",
            source: "nodes",
            layout: {
              "icon-image": ["coalesce", ["get", "__icon_image"], "node-pin"],
              "icon-size": 0.65,
              "icon-anchor": "bottom",
              "icon-allow-overlap": true
            }
          });
          map.addLayer({
            id: "way-emojis",
            type: "symbol",
            source: "way-emoji-points",
            layout: {
              "icon-image": "way-emoji",
              "icon-size": 0.82,
              "icon-allow-overlap": true
            }
          });
          map.addLayer({
            id: "relation-emojis",
            type: "symbol",
            source: "relation-emoji-points",
            layout: {
              "icon-image": "relation-emoji",
              "icon-size": 0.9,
              "icon-allow-overlap": true
            }
          });

          if ((normalized.features || []).length > 0) {
            const bbox = turf.bbox(normalized);
            if (bbox.every(Number.isFinite)) {
              map.fitBounds([[bbox[0], bbox[1]], [bbox[2], bbox[3]]], { padding: 24, duration: 0 });
            }

            const centerFeature = turf.center(normalized);
            const center = centerFeature && centerFeature.geometry && centerFeature.geometry.coordinates;
            if (Array.isArray(center) && center.length === 2 && center.every(Number.isFinite)) {
              map.flyTo({
                center: center,
                zoom: Math.max(map.getZoom(), 11),
                speed: 0.6,
                curve: 1.2,
                essential: true
              });
            }
          }

          document.getElementById("stats").textContent =
            "areas(relation+way): " + fillFeatures.length +
            " / nodes(total): " + convenienceNodeFeatures.length +
            " / 7-Eleven: " + iconCounts["cvs-711"] +
            " / FamilyMart: " + iconCounts["cvs-familymart"] +
            " / LAWSON: " + iconCounts["cvs-lawson"] +
            " / way(🛣️): " + wayEmojiPoints.length +
            " / relation(🧩): " + relationEmojiPoints.length;
        });
      })().catch((err) => {
        const stats = document.getElementById("stats");
        if (stats) stats.textContent = "render error: " + err.message;
      });
    </script>
  </body>
</html>
HTML

echo "done"
`, workName, endpoint, queryB64, centerLon, centerLat, zoom)
}

func buildStyleScript(workName, styleMode, styleJSON, styleURL string, centerLon, centerLat, zoom float64) string {
	styleExpr := fmt.Sprintf("%q", styleURL)
	styleB64 := ""
	if styleMode == "inline" {
		styleExpr = "\"./style.json\""
		styleB64 = base64.StdEncoding.EncodeToString([]byte(styleJSON))
	}

	return fmt.Sprintf(`set -euo pipefail
WORK=%q
OUT_DIR="/artifacts/${WORK}"
mkdir -p "${OUT_DIR}"

STYLE_MODE=%q
STYLE_B64=%q
STYLE_URL=%q

if [ "${STYLE_MODE}" = "inline" ]; then
  printf '%%s' "${STYLE_B64}" | base64 -d > "${OUT_DIR}/style.json"
fi

cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID style artifact</title>
    <link href="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.css" rel="stylesheet" />
    <style>
      html, body { margin: 0; height: 100%%; font-family: sans-serif; }
      #map { position: absolute; inset: 0; }
      #panel {
        position: absolute; z-index: 1; top: 12px; left: 12px;
        background: rgba(255,255,255,0.92); padding: 8px 10px; border-radius: 6px; font-size: 12px;
      }
    </style>
  </head>
  <body>
    <div id="panel">
      <strong>NEREID style preview</strong><br/>
      <a href="./style.json">style.json</a>
    </div>
    <div id="map"></div>
    <script src="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.js"></script>
    <script>
      new maplibregl.Map({
        container: "map",
        style: %s,
        center: [%f, %f],
        zoom: %f
      });
    </script>
  </body>
</html>
HTML

echo "done"
`, workName, styleMode, styleB64, styleURL, styleExpr, centerLon, centerLat, zoom)
}

func buildDuckdbScript(workName, inputURI, sql string, centerLon, centerLat, zoom float64) string {
	sqlB64 := base64.StdEncoding.EncodeToString([]byte(sql))
	return fmt.Sprintf(`set -euo pipefail
WORK=%q
OUT_DIR="/artifacts/${WORK}"
mkdir -p "${OUT_DIR}"

INPUT_URI=%q
SQL_B64=%q

printf '%%s' "${INPUT_URI}" > "${OUT_DIR}/input_uri.txt"
printf '%%s' "${SQL_B64}" | base64 -d > "${OUT_DIR}/query.sql"

cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID duckdb artifact</title>
    <link href="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.css" rel="stylesheet" />
    <style>
      html, body { margin: 0; height: 100%%; font-family: sans-serif; }
      #map { position: absolute; inset: 0; }
      #panel {
        position: absolute; z-index: 1; top: 12px; left: 12px;
        background: rgba(255,255,255,0.92); padding: 8px 10px; border-radius: 6px;
        font-size: 12px; max-width: min(460px, calc(100vw - 40px));
      }
      pre { white-space: pre-wrap; margin: 8px 0 0; max-height: 30vh; overflow: auto; }
    </style>
  </head>
  <body>
    <div id="panel">
      <strong>NEREID duckdb.map.v1 scaffold</strong><br/>
      <a href="./input_uri.txt">input_uri.txt</a> / <a href="./query.sql">query.sql</a><br/>
      This artifact currently scaffolds duckdb jobs and emits query inputs. Next step: execute query and render result points.
      <pre id="summary"></pre>
    </div>
    <div id="map"></div>
    <script src="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.js"></script>
    <script>
      const map = new maplibregl.Map({
        container: "map",
        style: {
          version: 8,
          sources: {
            osm: {
              type: "raster",
              tiles: [
                "https://a.tile.openstreetmap.org/{z}/{x}/{y}.png",
                "https://b.tile.openstreetmap.org/{z}/{x}/{y}.png",
                "https://c.tile.openstreetmap.org/{z}/{x}/{y}.png"
              ],
              tileSize: 256
            }
          },
          layers: [{ id: "osm", type: "raster", source: "osm" }]
        },
        center: [%f, %f],
        zoom: %f
      });

      (async function () {
        const [inputUri, query] = await Promise.all([
          fetch("./input_uri.txt").then((r) => r.text()),
          fetch("./query.sql").then((r) => r.text())
        ]);
        document.getElementById("summary").textContent =
          "input uri:\n" + inputUri + "\n\nsql:\n" + query;
      })().catch((e) => {
        document.getElementById("summary").textContent = "render error: " + e.message;
      });
    </script>
  </body>
</html>
HTML

echo "done"
`, workName, inputURI, sqlB64, centerLon, centerLat, zoom)
}

func buildGDALRasterScript(workName, inputURI, srcNoData, dstNoData, targetSRS, resampling string, minZoom, maxZoom int, centerLon, centerLat, zoom float64) string {
	return fmt.Sprintf(`set -euo pipefail
WORK=%q
OUT_DIR="/artifacts/${WORK}"
mkdir -p "${OUT_DIR}"

INPUT_URI=%q
SRC_NODATA=%q
DST_NODATA=%q
TARGET_SRS=%q
RESAMPLING=%q
MIN_ZOOM=%d
MAX_ZOOM=%d

echo "download source GeoTIFF..."
curl -fL "${INPUT_URI}" -o /tmp/input.tif

echo "inspect source GeoTIFF..."
gdalinfo /tmp/input.tif > "${OUT_DIR}/gdalinfo-input.txt"

IN_FILE=/tmp/input.tif
if [ -n "${DST_NODATA}" ]; then
  echo "apply nodata via gdal_translate..."
  gdal_translate -a_nodata "${DST_NODATA}" "${IN_FILE}" /tmp/input-nodata.tif
  IN_FILE=/tmp/input-nodata.tif
fi

echo "reproject with gdalwarp..."
if [ -n "${SRC_NODATA}" ] && [ -n "${DST_NODATA}" ]; then
  gdalwarp -r "${RESAMPLING}" -srcnodata "${SRC_NODATA}" -dstnodata "${DST_NODATA}" -t_srs "${TARGET_SRS}" "${IN_FILE}" /tmp/reprojected.tif
elif [ -n "${SRC_NODATA}" ]; then
  gdalwarp -r "${RESAMPLING}" -srcnodata "${SRC_NODATA}" -t_srs "${TARGET_SRS}" "${IN_FILE}" /tmp/reprojected.tif
elif [ -n "${DST_NODATA}" ]; then
  gdalwarp -r "${RESAMPLING}" -dstnodata "${DST_NODATA}" -t_srs "${TARGET_SRS}" "${IN_FILE}" /tmp/reprojected.tif
else
  gdalwarp -r "${RESAMPLING}" -t_srs "${TARGET_SRS}" "${IN_FILE}" /tmp/reprojected.tif
fi

echo "inspect reprojected GeoTIFF..."
gdalinfo /tmp/reprojected.tif > "${OUT_DIR}/gdalinfo-reprojected.txt"
cp /tmp/reprojected.tif "${OUT_DIR}/reprojected.tif"

echo "generate raster tiles..."
mkdir -p "${OUT_DIR}/tiles"
gdal2tiles.py -w none -z "${MIN_ZOOM}-${MAX_ZOOM}" /tmp/reprojected.tif "${OUT_DIR}/tiles"

cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID raster artifact</title>
    <link href="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.css" rel="stylesheet" />
    <style>
      html, body { margin: 0; height: 100%%; font-family: sans-serif; }
      #map { position: absolute; inset: 0; }
      #panel {
        position: absolute; z-index: 1; top: 12px; left: 12px;
        background: rgba(255,255,255,0.92); padding: 8px 10px; border-radius: 6px;
        font-size: 12px; max-width: min(460px, calc(100vw - 40px));
      }
      ul { margin: 6px 0 0; padding-left: 16px; }
    </style>
  </head>
  <body>
    <div id="panel">
      <strong>NEREID GDAL workflow artifact</strong><br/>
      GeoTIFF inspect -> NoData -> Reproject -> Raster tiles -> Web map
      <ul>
        <li><a href="./gdalinfo-input.txt">gdalinfo-input.txt</a></li>
        <li><a href="./gdalinfo-reprojected.txt">gdalinfo-reprojected.txt</a></li>
        <li><a href="./reprojected.tif">reprojected.tif</a></li>
        <li><a href="./tiles/">tiles/</a></li>
      </ul>
      <div id="status"></div>
    </div>
    <div id="map"></div>
    <script src="https://unpkg.com/maplibre-gl@4.7.1/dist/maplibre-gl.js"></script>
    <script>
      const map = new maplibregl.Map({
        container: "map",
        style: {
          version: 8,
          sources: {
            raster: {
              type: "raster",
              tiles: ["./tiles/{z}/{x}/{y}.png"],
              tileSize: 256
            }
          },
          layers: [{ id: "raster", type: "raster", source: "raster" }]
        },
        center: [%f, %f],
        zoom: %f
      });
      map.on("load", () => {
        document.getElementById("status").textContent = "raster tiles loaded";
      });
      map.on("error", (e) => {
        document.getElementById("status").textContent = "map error: " + (e && e.error ? e.error.message : "unknown");
      });
    </script>
  </body>
</html>
HTML

echo "done"
`, workName, inputURI, srcNoData, dstNoData, targetSRS, resampling, minZoom, maxZoom, centerLon, centerLat, zoom)
}

func buildLAZ3DTilesScript(workName, inputURI, sourceSRS, targetSRS, inAxisOrdering, outAxisOrdering string, pyprojAlwaysXY bool, py3dtilesJobs int, centerLon, centerLat, zoom float64) string {
	return fmt.Sprintf(`set -euo pipefail
WORK=%q
OUT_DIR="/artifacts/${WORK}"
mkdir -p "${OUT_DIR}"

INPUT_URI=%q
SOURCE_SRS=%q
TARGET_SRS=%q
IN_AXIS_ORDERING=%q
OUT_AXIS_ORDERING=%q
PYPROJ_ALWAYS_XY=%q
PY3DTILES_JOBS=%d

echo "download source LAZ..."
curl -fL "${INPUT_URI}" -o /tmp/input.laz

echo "inspect source LAZ metadata..."
pdal info /tmp/input.laz > "${OUT_DIR}/pdal-info-input.json"

python3 - <<'PY'
import json
import os

reproj = {
    "type": "filters.reprojection",
    "in_srs": os.environ["SOURCE_SRS"],
    "out_srs": os.environ["TARGET_SRS"],
}
if os.environ.get("IN_AXIS_ORDERING"):
    reproj["in_axis_ordering"] = os.environ["IN_AXIS_ORDERING"]
if os.environ.get("OUT_AXIS_ORDERING"):
    reproj["out_axis_ordering"] = os.environ["OUT_AXIS_ORDERING"]

pipeline = [
    {"type": "readers.las", "filename": "/tmp/input.laz"},
    reproj,
    {"type": "writers.las", "filename": "/tmp/reprojected.laz"},
]
with open("/tmp/pdal-pipeline.json", "w", encoding="utf-8") as f:
    json.dump(pipeline, f, indent=2)
PY

echo "run PDAL CRS conversion / axis-order correction..."
pdal pipeline /tmp/pdal-pipeline.json
pdal info /tmp/reprojected.laz > "${OUT_DIR}/pdal-info-reprojected.json"

if ! command -v py3dtiles >/dev/null 2>&1; then
  if command -v python3 >/dev/null 2>&1; then
    python3 -m pip install --no-cache-dir py3dtiles
  else
    echo "python3 is required to install py3dtiles" >&2
    exit 1
  fi
fi

echo "generate 3DTiles..."
mkdir -p "${OUT_DIR}/3dtiles"
if [ "${PYPROJ_ALWAYS_XY}" = "true" ]; then
  py3dtiles convert /tmp/reprojected.laz --out "${OUT_DIR}/3dtiles" --overwrite --jobs "${PY3DTILES_JOBS}" --srs_in "${TARGET_SRS}" --srs_out "${TARGET_SRS}" --pyproj-always-xy
else
  py3dtiles convert /tmp/reprojected.laz --out "${OUT_DIR}/3dtiles" --overwrite --jobs "${PY3DTILES_JOBS}" --srs_in "${TARGET_SRS}" --srs_out "${TARGET_SRS}"
fi

cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID pointcloud artifact</title>
    <script src="https://unpkg.com/cesium@1.117/Build/Cesium/Cesium.js"></script>
    <link href="https://unpkg.com/cesium@1.117/Build/Cesium/Widgets/widgets.css" rel="stylesheet"/>
    <style>
      html, body, #cesiumContainer { margin: 0; width: 100%%; height: 100%%; overflow: hidden; font-family: sans-serif; }
      #panel {
        position: absolute; z-index: 1; top: 12px; left: 12px;
        background: rgba(255,255,255,0.92); padding: 8px 10px; border-radius: 6px;
        font-size: 12px; max-width: min(460px, calc(100vw - 40px));
      }
      ul { margin: 6px 0 0; padding-left: 16px; }
    </style>
  </head>
  <body>
    <div id="panel">
      <strong>NEREID LAZ workflow artifact</strong><br/>
      LAZ metadata -> axis-order/CRS (PDAL) -> 3DTiles (py3dtiles) -> web visualization
      <ul>
        <li><a href="./pdal-info-input.json">pdal-info-input.json</a></li>
        <li><a href="./pdal-info-reprojected.json">pdal-info-reprojected.json</a></li>
        <li><a href="./3dtiles/tileset.json">3dtiles/tileset.json</a></li>
      </ul>
      <div id="status"></div>
    </div>
    <div id="cesiumContainer"></div>
    <script>
      window.CESIUM_BASE_URL = "https://unpkg.com/cesium@1.117/Build/Cesium/";
      (async function () {
        const viewer = new Cesium.Viewer("cesiumContainer", {
          timeline: false,
          animation: false,
          sceneModePicker: false,
          geocoder: false,
          homeButton: true,
          navigationHelpButton: false,
          baseLayerPicker: false
        });
        viewer.camera.setView({
          destination: Cesium.Cartesian3.fromDegrees(%f, %f, 2000000.0)
        });

        const tileset = await Cesium.Cesium3DTileset.fromUrl("./3dtiles/tileset.json");
        viewer.scene.primitives.add(tileset);
        await viewer.zoomTo(tileset);
        document.getElementById("status").textContent = "3DTiles loaded";
      })().catch((err) => {
        document.getElementById("status").textContent = "render error: " + err.message;
      });
    </script>
  </body>
</html>
HTML

echo "done"
`, workName, inputURI, sourceSRS, targetSRS, inAxisOrdering, outAxisOrdering, strconv.FormatBool(pyprojAlwaysXY), py3dtilesJobs, centerLon, centerLat)
}

func (c *Controller) updateWorkStatus(ctx context.Context, work *unstructured.Unstructured, phase, message, artifact string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest, err := c.dynamic.Resource(workGVR).Namespace(work.GetNamespace()).Get(ctx, work.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}

		currentPhase, _, _ := unstructured.NestedString(latest.Object, "status", "phase")
		currentMessage, _, _ := unstructured.NestedString(latest.Object, "status", "message")
		currentArtifact, _, _ := unstructured.NestedString(latest.Object, "status", "artifactUrl")
		if currentPhase == phase && currentMessage == message && currentArtifact == artifact {
			return nil
		}

		if err := unstructured.SetNestedField(latest.Object, phase, "status", "phase"); err != nil {
			return err
		}
		if message != "" {
			if err := unstructured.SetNestedField(latest.Object, message, "status", "message"); err != nil {
				return err
			}
		} else {
			unstructured.RemoveNestedField(latest.Object, "status", "message")
		}
		if artifact != "" {
			if err := unstructured.SetNestedField(latest.Object, artifact, "status", "artifactUrl"); err != nil {
				return err
			}
		} else {
			unstructured.RemoveNestedField(latest.Object, "status", "artifactUrl")
		}

		_, err = c.dynamic.Resource(workGVR).Namespace(work.GetNamespace()).UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}

func phaseFromJob(job *batchv1.Job) (string, string) {
	if job.Status.Succeeded > 0 {
		return "Succeeded", "job completed"
	}
	if job.Status.Failed > 0 {
		return "Failed", "job failed"
	}
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		return "Queued", "waiting for kueue admission"
	}
	if job.Status.Active > 0 {
		return "Running", "job is running"
	}
	return "Submitted", "job submitted"
}

func makeJobName(workName string) string {
	const prefix = "work-"
	const maxLen = 63
	maxBody := maxLen - len(prefix)

	workName = sanitizeDNSLabel(workName)
	if workName == "" {
		workName = "work"
	}
	if len(workName) <= maxBody {
		return prefix + workName
	}

	hash := sha1.Sum([]byte(workName))
	suffix := hex.EncodeToString(hash[:])[:8]
	bodyMax := maxBody - len(suffix) - 1
	if bodyMax < 1 {
		bodyMax = 1
	}

	body := strings.Trim(workName[:bodyMax], "-")
	if body == "" {
		body = "work"
	}
	return prefix + body + "-" + suffix
}

func artifactURL(base, workName string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/", base, workName)
}

func (c *Controller) validateGrantForWork(ctx context.Context, work *unstructured.Unstructured, kind string, grant *unstructured.Unstructured) error {
	if grant == nil {
		return nil
	}
	grantName := grant.GetName()

	enabled, found, err := unstructured.NestedBool(grant.Object, "spec", "enabled")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.enabled: %v", grantName, err)
	}
	if found && !enabled {
		return fmt.Errorf("grant %q is disabled", grantName)
	}

	expiresAt, _, err := unstructured.NestedString(grant.Object, "spec", "expiresAt")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.expiresAt: %v", grantName, err)
	}
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt != "" {
		ts, parseErr := time.Parse(time.RFC3339, expiresAt)
		if parseErr != nil {
			return fmt.Errorf("grant %q has invalid spec.expiresAt=%q (expected RFC3339): %v", grantName, expiresAt, parseErr)
		}
		now := c.nowFunc().UTC()
		if now.After(ts) {
			return fmt.Errorf("grant %q expired at %s", grantName, ts.UTC().Format(time.RFC3339))
		}
	}

	allowedKinds, _, err := unstructured.NestedStringSlice(grant.Object, "spec", "allowedKinds")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.allowedKinds: %v", grantName, err)
	}
	if len(allowedKinds) > 0 {
		ok := false
		for _, k := range allowedKinds {
			if strings.TrimSpace(k) == kind {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("grant %q does not allow spec.kind=%q", grantName, kind)
		}
	}

	maxUses, found, err := unstructured.NestedInt64(grant.Object, "spec", "maxUses")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.maxUses: %v", grantName, err)
	}
	if found && maxUses > 0 {
		jobs, listErr := c.kube.BatchV1().Jobs(c.cfg.JobNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("nereid.yuiseki.net/grant=%s", grantName),
		})
		if listErr != nil {
			return fmt.Errorf("list jobs for grant %q maxUses: %w", grantName, listErr)
		}
		used := int64(len(jobs.Items))
		if used >= maxUses {
			return fmt.Errorf("grant %q exhausted: maxUses=%d used=%d", grantName, maxUses, used)
		}
	}

	return nil
}

func allowedWorkNamesForGrantMaxUses(works []*unstructured.Unstructured, grantName string, maxUses int64) map[string]bool {
	out := map[string]bool{}
	grantName = strings.TrimSpace(grantName)
	if grantName == "" {
		return out
	}

	if maxUses <= 0 {
		for _, w := range works {
			if workGrantRefName(w) == grantName {
				out[w.GetName()] = true
			}
		}
		return out
	}

	candidates := make([]*unstructured.Unstructured, 0, len(works))
	for _, w := range works {
		if workGrantRefName(w) == grantName {
			candidates = append(candidates, w)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		ti := candidates[i].GetCreationTimestamp().Time
		tj := candidates[j].GetCreationTimestamp().Time
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return candidates[i].GetName() < candidates[j].GetName()
	})

	for i := range candidates {
		if int64(i) < maxUses {
			out[candidates[i].GetName()] = true
		}
	}
	return out
}

func workGrantRefName(work *unstructured.Unstructured) string {
	if work == nil {
		return ""
	}
	name, _, _ := unstructured.NestedString(work.Object, "spec", "grantRef", "name")
	return strings.TrimSpace(name)
}

func (c *Controller) applyGrantToJob(job *batchv1.Job, grant *unstructured.Unstructured) error {
	if job == nil || grant == nil {
		return nil
	}
	grantName := strings.TrimSpace(grant.GetName())

	if job.Labels == nil {
		job.Labels = map[string]string{}
	}
	if grantName != "" {
		job.Labels["nereid.yuiseki.net/grant"] = grantName
	}

	queueName, _, err := unstructured.NestedString(grant.Object, "spec", "kueue", "localQueueName")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.kueue.localQueueName: %v", grantName, err)
	}
	queueName = strings.TrimSpace(queueName)
	if queueName != "" {
		job.Labels["kueue.x-k8s.io/queue-name"] = queueName
	}

	runtimeClassName, _, err := unstructured.NestedString(grant.Object, "spec", "runtimeClassName")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.runtimeClassName: %v", grantName, err)
	}
	runtimeClassName = strings.TrimSpace(runtimeClassName)
	if runtimeClassName != "" {
		job.Spec.Template.Spec.RuntimeClassName = &runtimeClassName
	}

	if len(job.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("job has no containers")
	}
	container := &job.Spec.Template.Spec.Containers[0]
	if container.Resources.Requests == nil {
		container.Resources.Requests = corev1.ResourceList{}
	}
	if container.Resources.Limits == nil {
		container.Resources.Limits = corev1.ResourceList{}
	}

	reqCPU, _, err := nestedStringAny(grant.Object, "spec", "resources", "requests", "cpu")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.requests.cpu: %v", grantName, err)
	}
	reqMem, _, err := nestedStringAny(grant.Object, "spec", "resources", "requests", "memory")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.requests.memory: %v", grantName, err)
	}
	limCPU, _, err := nestedStringAny(grant.Object, "spec", "resources", "limits", "cpu")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.limits.cpu: %v", grantName, err)
	}
	limMem, _, err := nestedStringAny(grant.Object, "spec", "resources", "limits", "memory")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.limits.memory: %v", grantName, err)
	}

	if strings.TrimSpace(reqCPU) != "" {
		q, parseErr := resource.ParseQuantity(reqCPU)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.requests.cpu=%q: %v", grantName, reqCPU, parseErr)
		}
		container.Resources.Requests[corev1.ResourceCPU] = q
	}
	if strings.TrimSpace(reqMem) != "" {
		q, parseErr := resource.ParseQuantity(reqMem)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.requests.memory=%q: %v", grantName, reqMem, parseErr)
		}
		container.Resources.Requests[corev1.ResourceMemory] = q
	}
	if strings.TrimSpace(limCPU) != "" {
		q, parseErr := resource.ParseQuantity(limCPU)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.limits.cpu=%q: %v", grantName, limCPU, parseErr)
		}
		container.Resources.Limits[corev1.ResourceCPU] = q
	}
	if strings.TrimSpace(limMem) != "" {
		q, parseErr := resource.ParseQuantity(limMem)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.limits.memory=%q: %v", grantName, limMem, parseErr)
		}
		container.Resources.Limits[corev1.ResourceMemory] = q
	}

	envVars, err := grantEnvVars(grant)
	if err != nil {
		return err
	}
	if len(envVars) > 0 {
		// Override by name to avoid duplicates.
		existing := make([]corev1.EnvVar, 0, len(container.Env))
		toDrop := map[string]bool{}
		for _, ev := range envVars {
			toDrop[ev.Name] = true
		}
		for _, ev := range container.Env {
			if !toDrop[ev.Name] {
				existing = append(existing, ev)
			}
		}
		container.Env = append(existing, envVars...)
	}

	return nil
}

func grantEnvVars(grant *unstructured.Unstructured) ([]corev1.EnvVar, error) {
	if grant == nil {
		return nil, nil
	}
	grantName := grant.GetName()
	raw, found, err := unstructured.NestedSlice(grant.Object, "spec", "env")
	if err != nil {
		return nil, fmt.Errorf("failed to read grant %q spec.env: %v", grantName, err)
	}
	if !found || len(raw) == 0 {
		return nil, nil
	}

	out := make([]corev1.EnvVar, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("grant %q spec.env[%d] must be an object", grantName, i)
		}
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("grant %q spec.env[%d].name is required", grantName, i)
		}

		if v, ok := m["value"].(string); ok {
			out = append(out, corev1.EnvVar{Name: name, Value: v})
			continue
		}

		if skr, ok := m["secretKeyRef"].(map[string]interface{}); ok {
			sec, _ := skr["name"].(string)
			key, _ := skr["key"].(string)
			sec = strings.TrimSpace(sec)
			key = strings.TrimSpace(key)
			if sec == "" || key == "" {
				return nil, fmt.Errorf("grant %q spec.env[%d].secretKeyRef.name and key are required", grantName, i)
			}

			var optional *bool
			if ov, ok := skr["optional"].(bool); ok {
				optional = &ov
			}

			out = append(out, corev1.EnvVar{
				Name: name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: sec},
						Key:                  key,
						Optional:             optional,
					},
				},
			})
			continue
		}

		return nil, fmt.Errorf("grant %q spec.env[%d] must set value or secretKeyRef", grantName, i)
	}
	return out, nil
}

func extractDeadlineSeconds(work *unstructured.Unstructured) int64 {
	const fallback int64 = 600
	d, found, err := unstructured.NestedInt64(work.Object, "spec", "constraints", "deadlineSeconds")
	if err != nil || !found || d <= 0 {
		return fallback
	}
	return d
}

func extractViewport(work *unstructured.Unstructured) (lon, lat, zoom float64) {
	const (
		defaultLon  = 139.76
		defaultLat  = 35.68
		defaultZoom = 11.0
	)
	lon, lat, zoom = defaultLon, defaultLat, defaultZoom

	centerField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "render", "viewport", "center")
	if err == nil && found {
		if center, ok := centerField.([]interface{}); ok && len(center) == 2 {
			if v, ok := toFloat64(center[0]); ok {
				lon = v
			}
			if v, ok := toFloat64(center[1]); ok {
				lat = v
			}
		}
	}

	zoomField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "render", "viewport", "zoom")
	if err == nil && found {
		if v, ok := toFloat64(zoomField); ok && v > 0 {
			zoom = v
		}
	}

	return lon, lat, zoom
}

func extractTileZoomRange(work *unstructured.Unstructured) (minZoom, maxZoom int) {
	const (
		defaultMin = 0
		defaultMax = 14
	)
	minZoom, maxZoom = defaultMin, defaultMax

	minField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "raster", "tiles", "minZoom")
	if err == nil && found {
		if v, ok := toFloat64(minField); ok {
			minZoom = int(v)
		}
	}
	maxField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "raster", "tiles", "maxZoom")
	if err == nil && found {
		if v, ok := toFloat64(maxField); ok {
			maxZoom = int(v)
		}
	}

	if minZoom < 0 {
		minZoom = 0
	}
	if maxZoom < minZoom {
		maxZoom = minZoom
	}
	if maxZoom > 24 {
		maxZoom = 24
	}
	return minZoom, maxZoom
}

func extractPointcloudJobs(work *unstructured.Unstructured) int {
	const defaultJobs = 2
	jobs := defaultJobs
	v, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "pointcloud", "py3dtiles", "jobs")
	if err != nil || !found {
		return jobs
	}
	if f, ok := toFloat64(v); ok {
		jobs = int(f)
	}
	if jobs < 1 {
		jobs = 1
	}
	if jobs > 64 {
		jobs = 64
	}
	return jobs
}

func nestedStringAny(obj map[string]interface{}, fields ...string) (string, bool, error) {
	v, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found || v == nil {
		return "", found, err
	}

	switch s := v.(type) {
	case string:
		return s, true, nil
	default:
		return fmt.Sprintf("%v", s), true, nil
	}
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case uint32:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func int32Ptr(v int32) *int32 {
	return &v
}

func mustParseQuantity(v string) resource.Quantity {
	return resource.MustParse(v)
}

func (c *Controller) pruneArtifacts() error {
	if c.cfg.ArtifactsHostPath == "" || c.cfg.ArtifactRetention <= 0 {
		return nil
	}

	entries, err := os.ReadDir(c.cfg.ArtifactsHostPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read artifacts root %q: %w", c.cfg.ArtifactsHostPath, err)
	}

	cutoff := c.nowFunc().Add(-c.cfg.ArtifactRetention)
	for _, entry := range entries {
		path := filepath.Join(c.cfg.ArtifactsHostPath, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			c.logger.Warn("skip artifact entry due to stat error", "path", path, "error", infoErr)
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}

		if removeErr := os.RemoveAll(path); removeErr != nil {
			c.logger.Warn("failed to remove expired artifact entry", "path", path, "error", removeErr)
			continue
		}
		c.logger.Info("pruned expired artifact entry", "path", path, "modTime", info.ModTime(), "retention", c.cfg.ArtifactRetention)
	}
	return nil
}

func sanitizeDNSLabel(v string) string {
	v = strings.ToLower(v)
	var b strings.Builder
	b.Grow(len(v))
	lastHyphen := false
	for _, r := range v {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if isLower || isDigit {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}
