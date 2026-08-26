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
		$(COMPOSE) -f $(COMPOSE_FILE) exec -T postgres \
			psql -v ON_ERROR_STOP=1 -U yfitops -d yfitops < $$m || exit 1; \
	done

test:
	cd $(SERVER_DIR) && go test ./...

clean:
	$(COMPOSE) -f $(COMPOSE_FILE) down -v
