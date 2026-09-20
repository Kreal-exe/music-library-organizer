# The agent is embedded into the application, so it is always built first:
# an application built against a stale agent reads tags on the phone with old
# code, and files the desktop handles fine start failing there.

AGENT := internal/device/agent/mlm-agent-arm64

.PHONY: build agent icon test dev clean

build: agent
	wails build

agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(AGENT) ./cmd/agent

icon:
	go run ./tools/icon build

test: agent
	go test ./...

dev: agent
	wails dev

clean:
	rm -rf build/bin $(AGENT)
