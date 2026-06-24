# yfitops V2 — infra convenience targets. Run from deploy/.
#   make up      build + start postgres, redis, gameserver
#   make down    stop and remove containers (volumes kept)
#   make logs     tail all service logs
#   make migrate  (re)apply migrations to the running postgres
#   make test     run the Go test suite
#   make clean    down + drop volumes (wipes pg data + dev certs)

COMPOSE ?= docker compose
COMPOSE_FILE := docker-compose.yml
SERVER_DIR := ../server

.PHONY: up down logs migrate test clean ps config

up:
	$(COMPOSE) -f $(COMPOSE_FILE) up --build -d

down:
	$(COMPOSE) -f $(COMPOSE_FILE) down

logs:
	$(COMPOSE) -f $(COMPOSE_FILE) logs -f

ps:
	$(COMPOSE) -f $(COMPOSE_FILE) ps

config:
	$(COMPOSE) -f $(COMPOSE_FILE) config

# Re-apply migrations against the running postgres container. The init mount
# only runs on first boot, so use this to apply schema to an existing volume.
# Idempotent: 0001_init.sql uses CREATE TABLE IF NOT EXISTS.
migrate:
	$(COMPOSE) -f $(COMPOSE_FILE) exec -T postgres \
		psql -v ON_ERROR_STOP=1 -U yfitops -d yfitops < migrations/0001_init.sql

test:
	cd $(SERVER_DIR) && go test ./...

clean:
	$(COMPOSE) -f $(COMPOSE_FILE) down -v
