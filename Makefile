PROJECT_NAME := template-go

.PHONY: build-server
build-server:
	@docker run --rm -v ./:/code -w /code golang:1.21-alpine \
	go mod tidy && \
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ./dist/server ./cmd/server/main.go

.PHONY: test
test:
	@docker run --rm -v ./:/code -w /code golang:1.21-alpine go test ./...

.PHONY: get-pkg
get-pkg:
	@echo 'pkg name?' && \
	read pkg_name && \
	docker run --rm -v ./:/code -w /code golang:1.21-alpine \
	go mod tidy && \
	go get -u $${pkg_name}

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
	@echo 'service name?' && \
	read service_name && \
	docker restart ${PROJECT_NAME}.$${service_name}

.PHONY: exec
exec:
	@echo 'service name?' && \
	read service_name && \
	docker exec -it ${PROJECT_NAME}.$${service_name} ash

.PHONY: logs
logs:
	@echo 'service name?' && \
	read service_name && \
	docker logs -f -n 50 ${PROJECT_NAME}.$${service_name}

.PHONY: stats
stats:
	@echo 'service name?' && \
	read service_name && \
	docker stats ${PROJECT_NAME}.$${service_name}
