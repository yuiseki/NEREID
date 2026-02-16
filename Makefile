SHELL := /bin/bash

NAMESPACE ?= nereid
API_DEPLOYMENT ?= nereid-api
CONTROLLER_DEPLOYMENT ?= nereid-controller
AGENT_IMAGE ?= nereid-agent-runtime:local
PLAYWRIGHT_CHROMIUM ?= 0

.PHONY: build deploy build-agent-image

build:
	go build -o ./bin/nereid-api ./cmd/nereid-api
	go build -o ./bin/nereid-controller ./cmd/nereid-controller

build-agent-image:
	docker build -f Dockerfile.agent-runtime -t $(AGENT_IMAGE) --build-arg INSTALL_PLAYWRIGHT_CHROMIUM=$(PLAYWRIGHT_CHROMIUM) .

deploy: build
	kubectl -n $(NAMESPACE) rollout restart deployment/$(API_DEPLOYMENT) deployment/$(CONTROLLER_DEPLOYMENT)
	kubectl -n $(NAMESPACE) rollout status deployment/$(API_DEPLOYMENT) --timeout=180s
	kubectl -n $(NAMESPACE) rollout status deployment/$(CONTROLLER_DEPLOYMENT) --timeout=180s
