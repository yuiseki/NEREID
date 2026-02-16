SHELL := /bin/bash

NAMESPACE ?= nereid
API_DEPLOYMENT ?= nereid-api
CONTROLLER_DEPLOYMENT ?= nereid-controller
AGENT_IMAGE ?= nereid-agent-runtime:local
CTR_NAMESPACE ?= k8s.io
PLAYWRIGHT_CHROMIUM ?= 1

.PHONY: build build-go deploy build-agent-image import-agent-image

build: build-go build-agent-image

build-go:
	go build -o ./bin/nereid-api ./cmd/nereid-api
	go build -o ./bin/nereid-controller ./cmd/nereid-controller

build-agent-image:
	docker build -f Dockerfile.agent-runtime -t $(AGENT_IMAGE) --build-arg INSTALL_PLAYWRIGHT_CHROMIUM=$(PLAYWRIGHT_CHROMIUM) .

import-agent-image:
	set -o pipefail; docker save $(AGENT_IMAGE) | ctr -n $(CTR_NAMESPACE) images import -

deploy: build import-agent-image
	kubectl -n $(NAMESPACE) rollout restart deployment/$(API_DEPLOYMENT) deployment/$(CONTROLLER_DEPLOYMENT)
	kubectl -n $(NAMESPACE) rollout status deployment/$(API_DEPLOYMENT) --timeout=180s
	kubectl -n $(NAMESPACE) rollout status deployment/$(CONTROLLER_DEPLOYMENT) --timeout=180s
