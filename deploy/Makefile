# yfitops V2 — infra convenience targets. Run from deploy/.
#   make up      build + start postgres, redis, gameserver
#   make down    stop and remove containers (volumes kept)
#   make logs     tail all service logs
#   make migrate  (re)apply migrations to the running postgres
#   make test     run the Go test suite
#   make clean    down + drop volumes (wipes pg data + dev certs)

COMPOSE ?= docker compose
COMPOSE_FILE := docker-compose.yml
OVERRIDE_FILE := docker-compose.override.yml
# Every target below passes an explicit -f, which suppresses Compose's own
# default auto-discovery of docker-compose.override.yml (e.g. prod's real
# Let's Encrypt cert bind-mount, replacing the self-signed /certs volume).
# Re-include it here whenever it's present so `make up` can't silently drop
# it again -- this is what caused the gameserver's "failed to find any PEM
# data" crash loop (2026-10-17/21): the override existed on disk the whole
# time, `make up` just never looked at it.
COMPOSE_FILES := -f $(COMPOSE_FILE) $(if $(wildcard $(OVERRIDE_FILE)),-f $(OVERRIDE_FILE))
SERVER_DIR := ../server

.PHONY: up down logs migrate test clean ps config

up:
	$(COMPOSE) $(COMPOSE_FILES) up --build -d

down:
	$(COMPOSE) $(COMPOSE_FILES) down

logs:
	$(COMPOSE) $(COMPOSE_FILES) logs -f

ps:
	$(COMPOSE) $(COMPOSE_FILES) ps

config:
	$(COMPOSE) $(COMPOSE_FILES) config

# Re-apply migrations against the running postgres container. The init mount
# only runs on first boot, so use this to apply schema to an existing volume.
# Every migration must be idempotent (CREATE ... IF NOT EXISTS etc.) because
# this reapplies all of them every time.
#
# Globs migrations/*.sql rather than listing files: the list previously stopped
# at 0005, so a newly added migration was silently skipped by `make migrate`
# while scripts/dev-up.sh (which already globs) applied it. Lexical order is
# the intended apply order.
migrate:
	@for m in migrations/*.sql; do \
		echo "==> applying $$m"; \
		$(COMPOSE) $(COMPOSE_FILES) exec -T postgres \
			psql -v ON_ERROR_STOP=1 -U yfitops -d yfitops < $$m || exit 1; \
	done

test:
	cd $(SERVER_DIR) && go test ./...

clean:
	$(COMPOSE) $(COMPOSE_FILES) down -v
