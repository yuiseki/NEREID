#!/usr/bin/env bash
# scripts/setup.sh — Environment setup for NEREID workspace
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

echo "==> Setting up NEREID workspace..."

# Install Node.js dependencies
cd "$PROJECT_DIR"
npm install

# Create public directory for static assets if not exists
mkdir -p "$PROJECT_DIR/public"

echo "==> Setup complete."
echo "    Run 'make dev' to start the development server."
echo "    Run 'make build' to create a production build."
