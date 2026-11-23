.PHONY: deps run run-local test test-offline up down fmt

# First time: fetch the AWS SDK and write go.sum.
deps:
	go mod tidy

# Run against S3 (needs S3_BUCKET + AWS credentials in the environment).
run:
	go run ./cmd/server

# Run with files on local disk instead of S3: no AWS account needed.
run-local:
	STORAGE=local go run ./cmd/server

test:
	go test -race ./...

# Same tests without compiling the AWS SDK.
test-offline:
	go test -race -tags nos3 ./...

# API + MinIO in Docker.
up:
	docker compose up --build

down:
	docker compose down

fmt:
	gofmt -w .
