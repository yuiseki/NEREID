SHELL := /bin/bash

NAMESPACE ?= nereid
API_DEPLOYMENT ?= nereid-api
CONTROLLER_DEPLOYMENT ?= nereid-controller
AGENT_IMAGE ?= nereid-agent-runtime:local
CTR_NAMESPACE ?= k8s.io
HELM_RELEASE ?= nereid
HELM_CHART ?= ./charts/nereid
HELM_VALUES ?= -f charts/nereid/values.yaml
HELM_TIMEOUT ?= 180s
HELM_UPGRADE_ARGS ?=
PLAYWRIGHT_CHROMIUM ?= 1

.PHONY: build build-go deploy build-agent-image import-agent-image helm-upgrade

build: build-go build-agent-image

build-go:
	go build -o ./bin/nereid-api ./cmd/nereid-api
	go build -o ./bin/nereid-controller ./cmd/nereid-controller

build-agent-image:
	docker build -f Dockerfile.agent-runtime -t $(AGENT_IMAGE) --build-arg INSTALL_PLAYWRIGHT_CHROMIUM=$(PLAYWRIGHT_CHROMIUM) .

import-agent-image:
	set -o pipefail; docker save $(AGENT_IMAGE) | ctr -n $(CTR_NAMESPACE) images import -

helm-upgrade:
	helm upgrade --install $(HELM_RELEASE) $(HELM_CHART) -n $(NAMESPACE) --create-namespace --wait --timeout $(HELM_TIMEOUT) $(HELM_VALUES) $(HELM_UPGRADE_ARGS)

deploy: build import-agent-image helm-upgrade
	kubectl -n $(NAMESPACE) rollout restart deployment/$(API_DEPLOYMENT) deployment/$(CONTROLLER_DEPLOYMENT)
	kubectl -n $(NAMESPACE) rollout status deployment/$(API_DEPLOYMENT) --timeout=180s
	kubectl -n $(NAMESPACE) rollout status deployment/$(CONTROLLER_DEPLOYMENT) --timeout=180s
