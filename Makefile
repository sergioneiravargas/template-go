GO_MODULE_NAME := github.com/sergioneiravargas/template-go

include .env
export

.PHONY: run
run: build up

.PHONY: build
build: build-server

.PHONY: build-server
build-server:
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ./bin/server ./cmd/server/main.go

.PHONY: up
up:
	@docker compose -f docker-compose.yaml -f docker-compose.yaml.local up --build --remove-orphans

.PHONY: down
down:
	@docker compose -f docker-compose.yaml -f docker-compose.yaml.local down

.PHONY: exec
exec:
	@echo 'service name:' && read service_name && \
	docker exec -it ${APP_NAME}.$${service_name} ash

.PHONY: logs
logs:
	@echo 'service name:' && read service_name && \
	docker logs -f -n 50 ${APP_NAME}.$${service_name}

.PHONY: stats
stats:
	@echo 'service name:' && read service_name && \
	docker stats ${APP_NAME}.$${service_name}

.PHONY: loc
loc:
	@find ./ -name '*.go' | xargs wc -l | tail -1

.PHONY: test
test:
	@go test -v ./...

# Don't forget to run this command before making any changes to the project
.PHONY: init
init:
	@echo 'go module name?' && read new_go_module_name && \
	find . -type d -name .git -prune -type f -name .git -prune -o -type f -exec sed -i "s|${GO_MODULE_NAME}|$${new_go_module_name}|g" {} + && \
	head -n -7 Makefile > Makefile.tmp && mv Makefile.tmp Makefile
