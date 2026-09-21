# The agent is embedded into the application, so it is always built first:
# an application built against a stale agent reads tags on the phone with old
# code, and files the desktop handles fine start failing there.

AGENT := internal/device/agent/mlm-agent-arm64
VERSION := $(shell sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json)
MAC_APP := build/bin/MusicLibraryOrganizer.app
MAC_ZIP := build/bin/MusicLibraryOrganizer-$(VERSION)-macos-universal.zip

.PHONY: build agent mac mac-zip icon test dev clean

build: agent
	wails build

# One application for Apple Silicon and Intel alike, signed ad hoc.
mac: agent
	rm -rf "$(MAC_APP)"
	-wails build -platform darwin/universal
	@for try in 1 2 3 4 5; do \
		xattr -cr "$(MAC_APP)" && codesign --force --deep --sign - "$(MAC_APP)" 2>/dev/null && break; \
		[ $$try = 5 ] && { echo "codesign kept failing"; exit 1; }; sleep 1; \
	done
	codesign --verify --deep "$(MAC_APP)"
	@echo "Built $(MAC_APP)"

# The download for a release: signed and checked in a temporary folder, then
# zipped.
mac-zip: mac
	@stage=$$(mktemp -d) && app="$$stage/$(notdir $(MAC_APP))" && \
	ditto --norsrc --noextattr "$(MAC_APP)" "$$app" && \
	codesign --force --deep --sign - "$$app" && \
	codesign --verify --deep --strict "$$app" && \
	ditto -c -k --sequesterRsrc --keepParent "$$app" "$$stage/out.zip" && \
	mv "$$stage/out.zip" "$(MAC_ZIP)" && rm -rf "$$stage"
	@echo "Built $(MAC_ZIP)"

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
