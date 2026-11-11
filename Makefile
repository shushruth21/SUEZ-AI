COMPOSE_FILE := infra/compose/docker-compose.yml
PATH := $(HOME)/.docker/bin:$(PATH)
export PATH
ENV_FILE := $(wildcard .env)
COMPOSE := docker compose $(if $(ENV_FILE),--env-file $(ENV_FILE),) -f $(COMPOSE_FILE)

.PHONY: build up down deploy logs ps clean dmg staging

build:
	$(COMPOSE) build

up:
	$(COMPOSE) up --build

deploy: build
	$(COMPOSE) up -d

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f

ps:
	$(COMPOSE) ps

clean:
	$(COMPOSE) down -v --remove-orphans

# Context Guardian installer, copied to ./ContextGuardian.dmg
dmg:
	sh scripts/build-dmg.sh

# Staging shared with testers (see docs/TESTING-STAGING.md)
staging:
	bash scripts/staging.sh up
