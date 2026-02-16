SHELL := /bin/bash

NAMESPACE ?= nereid
API_DEPLOYMENT ?= nereid-api
CONTROLLER_DEPLOYMENT ?= nereid-controller

.PHONY: build deploy

build:
	go build -o ./bin/nereid-api ./cmd/nereid-api
	go build -o ./bin/nereid-controller ./cmd/nereid-controller

deploy: build
	kubectl -n $(NAMESPACE) rollout restart deployment/$(API_DEPLOYMENT) deployment/$(CONTROLLER_DEPLOYMENT)
	kubectl -n $(NAMESPACE) rollout status deployment/$(API_DEPLOYMENT) --timeout=180s
	kubectl -n $(NAMESPACE) rollout status deployment/$(CONTROLLER_DEPLOYMENT) --timeout=180s
