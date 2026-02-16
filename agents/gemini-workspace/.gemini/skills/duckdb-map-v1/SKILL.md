---
name: duckdb-map-v1
description: Decide when DuckDB is appropriate and how to prepare query-to-map workflows.
---
# DuckDB Map Workflow

## When to use
- User instruction implies tabular/spatial analytics before visualization.
- Data source is parquet/csv/geo-like tabular input needing SQL summarization/filtering.

## Core knowledge
- DuckDB is strong for local analytical SQL.
- Query outputs often need conversion to GeoJSON or coordinate columns for mapping.
- Keep queries deterministic and readable.

## Recommended workflow
1. Persist input URI(s) and SQL for reproducibility.
2. Execute query when runtime supports DuckDB; otherwise provide structured fallback.
3. Convert results into map-ready data representation.
4. Render output and query summary in index.html.

## Output expectations
- Keep input/query artifacts inspectable.
- Keep map/status page usable even when execution is partially unavailable.
