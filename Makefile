.PHONY: fmt vet lint test race cover build clean

# gofmt -l -w formats every source file.
fmt:
	gofmt -l -w .

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test -count=1 ./...

# race runs tests with the race detector.
race:
	go test -count=1 -race ./...

cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

build:
	go build -o ivy-server ./cmd/serve

clean:
	rm -f ivy-server coverage.out
