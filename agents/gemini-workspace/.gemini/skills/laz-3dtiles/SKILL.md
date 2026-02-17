---
name: laz-3dtiles
description: Decide when LAZ to 3DTiles flow is needed and how to structure 3D pointcloud outputs.
---
# LAZ to 3DTiles Pipeline

## When to use
- User requests interactive 3D pointcloud visualization from LAZ/LAS data.
- CRS normalization and tileset generation are needed for web viewers.

## Core knowledge
- Pointcloud workflows often require CRS checks/reprojection.
- 3DTiles output should be accompanied by a browser preview and metadata.

## Recommended workflow
1. Validate source file and CRS assumptions.
2. Run conversion pipeline to 3DTiles when toolchain is available.
3. Produce browser-viewable entrypoint (Cesium or equivalent).
4. Include links to generated tileset and metadata.

## Output expectations
- index.html must remain usable.
- If conversion toolchain is unavailable, provide explicit fallback details in-page.
