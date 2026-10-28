.PHONY: build check release

build:
	CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o bin/garmin-mcp ./cmd/garmin-mcp

check:
	test -z "$$(gofmt -l cmd internal)"
	go test -mod=readonly -race ./...
	go vet ./...
	go mod verify
	python3 -m unittest discover -s scripts -p 'test_*.py'

release:
	python3 scripts/release.py
