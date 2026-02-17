.PHONY: install dev build clean typecheck help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

install: ## Install npm dependencies
	npm install

dev: install ## Start Vite dev server
	npm run dev

build: install ## Build for production and copy dist to root
	npm run build
	cp -r dist/. ./
	@echo "Build complete. dist/ contents copied to project root."

typecheck: ## Run TypeScript type checking
	npx tsc --noEmit

clean: ## Remove build artifacts and node_modules
	rm -rf dist node_modules
	@echo "Cleaned dist/ and node_modules/"

fetch-geojson: ## Fetch GeoJSON data using Python script
	python3 scripts/fetch_geojson.py $(ARGS)
