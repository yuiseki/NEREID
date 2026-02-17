---
name: gdal-rastertile
description: Decide when raster tiling is needed and how to structure GDAL-based pipelines.
---
# GDAL Raster Pipeline

## When to use
- Input is raster imagery (GeoTIFF etc.) and user needs web tile visualization.
- Reprojection, nodata handling, or zoom-range control is required.

## Core knowledge
- Typical steps: inspect -> optional nodata normalization -> reprojection -> tile generation.
- Output should include both artifacts and a preview map.

## Recommended workflow
1. Capture source metadata and processing parameters.
2. Apply necessary raster transforms.
3. Generate web-consumable tiles.
4. Provide index.html preview and links to intermediate artifacts.

## Output expectations
- Reproducible pipeline artifacts.
- Clear fallback message when toolchain/runtime is unavailable.
