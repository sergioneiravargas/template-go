PROJECT_NAME := template-go

.PHONY: setup
setup: setup-env setup-docker

.PHONY: build
build: build-server

.PHONY: setup-env
setup-env:
	@> .env && \
	echo "APP_NAME=${PROJECT_NAME}" >> .env && \
	echo 'APP_ENV:' && read app_env && echo "APP_ENV=$${app_env}" >> .env && \
	echo 'SQL_USER:' && read sql_user && echo "SQL_USER=$${sql_user}" >> .env && \
	echo 'SQL_PASSWORD:' && read sql_password && echo "SQL_PASSWORD=$${sql_password}" >> .env && \
	echo 'SQL_HOST:' && read sql_host && echo "SQL_HOST=$${sql_host}" >> .env && \
	echo 'SQL_PORT:' && read sql_port && echo "SQL_PORT=$${sql_port}" >> .env && \
	echo 'SQL_NAME:' && read sql_name && echo "SQL_NAME=$${sql_name}" >> .env && \
	echo 'JWT_KEYSET_URL:' && read jwt_keyset_url && echo "JWT_KEYSET_URL=$${jwt_keyset_url}" >> .env

.PHONY: setup-docker
setup-docker:
	@[ -e ./docker-compose.yaml.local ] || cp ./docker-compose.yaml.local.dist ./docker-compose.yaml.local

.PHONY: build-server
build-server:
	@docker run --rm -v ./:/code -w /code \
	-e CGO_ENABLED=0 \
	-e GOOS=linux \
	-e GOARCH=amd64 \
	golang:1.21-alpine \
	go build -o ./dist/server ./cmd/server/main.go

.PHONY: go
go:
	@echo 'go command:' && \
	read go_command && \
	docker run --rm -v ./:/code -w /code golang:1.21-alpine \
	go $${go_command}

.PHONY: loc
loc:
	@find ./ -name '*.go' | xargs wc -l | tail -1

.PHONY: up
up:
	@docker compose -f docker-compose.yaml -f docker-compose.yaml.local up -d --build --remove-orphans

.PHONY: down
down:
	@docker compose -f docker-compose.yaml -f docker-compose.yaml.local down

.PHONY: restart
restart:
	@echo 'service name:' && \
	read service_name && \
	docker restart ${PROJECT_NAME}.$${service_name}

.PHONY: exec
exec:
	@echo 'service name:' && \
	read service_name && \
	docker exec -it ${PROJECT_NAME}.$${service_name} ash

.PHONY: logs
logs:
	@echo 'service name:' && \
	read service_name && \
	docker logs -f -n 50 ${PROJECT_NAME}.$${service_name}

.PHONY: stats
stats:
	@echo 'service name:' && \
	read service_name && \
	docker stats ${PROJECT_NAME}.$${service_name}
