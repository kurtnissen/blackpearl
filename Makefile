.PHONY: test coverage vet lint verify compose-check docker docker-poc fuse-smoke

test:
	go test -race ./...

coverage:
	go test -coverprofile=coverage.out ./...
	./scripts/check-coverage.sh coverage.out

vet:
	go vet ./...

lint:
	golangci-lint run

verify: vet test coverage

compose-check:
	./scripts/test-compose-paths.sh

docker:
	docker build --target runtime -t blackpearl:local .

docker-poc:
	docker build --target poc -t blackpearl:poc .

fuse-smoke:
	BLACKPEARL_FUSE_TEST=1 go test -race ./internal/pearlfs -run TestMountedFilesystemServesExactBytesAndOffsetReads
