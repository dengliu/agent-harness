# Development stack for the ReAct agent demo.
#
#   make up        start the Go agent (air live-reload) and the Next.js UI
#   make down      stop them
#
# Both services run from this working tree via bind mounts; nothing is built
# into an image. See compose.yaml.

COMPOSE ?= docker compose

.DEFAULT_GOAL := help

## up: start the stack and stream its logs (Ctrl-C to stop)
up:
	$(COMPOSE) up --remove-orphans

## up-d: start the stack in the background
up-d:
	$(COMPOSE) up -d --remove-orphans
	@echo
	@echo "  UI   http://localhost:3000"
	@echo "  API  http://localhost:8080/api/health"

## down: stop the stack
down:
	$(COMPOSE) down --remove-orphans

## logs: follow the logs of both services
logs:
	$(COMPOSE) logs -f

## ps: show what is running
ps:
	$(COMPOSE) ps

## restart: restart the Go agent (air handles code changes on its own)
restart:
	$(COMPOSE) restart agent

## sh: open a shell in the agent container, e.g. to run go test
sh:
	$(COMPOSE) exec agent sh

## test: run the Go tests inside the container
test:
	$(COMPOSE) run --rm --no-deps agent go test ./...

## ask: one-shot question in the terminal, e.g. make ask Q="what time is it in Tokyo?"
ask:
	$(COMPOSE) run --rm --no-deps -T agent go run . -q "$(Q)"

## clean: stop the stack and delete its caches and node_modules volumes
clean:
	$(COMPOSE) down -v --remove-orphans
	rm -rf .air-tmp

## help: list the targets
help:
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'

.PHONY: up up-d down logs ps restart sh test ask clean help
