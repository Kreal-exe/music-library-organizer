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

# One application for Apple Silicon and Intel alike.
#
# In a folder synced by iCloud Drive, as ~/Documents often is, the bundle gets
# tagged with Finder information that codesign refuses, so Wails' own signing
# step fails there. Its failure is let through and the bundle signed here; a
# build that really failed leaves no application and still stops the run.
# iCloud tags the bundle again straight away, so the check here is the one
# that matters for running it, not the strict one a download has to pass.
mac: agent
	rm -rf "$(MAC_APP)"
	-wails build -platform darwin/universal
	xattr -cr "$(MAC_APP)"
	codesign --force --deep --sign - "$(MAC_APP)"
	codesign --verify --deep "$(MAC_APP)"
	@echo "Built $(MAC_APP)"

# The download for a release. It is signed and zipped in a temporary folder
# outside iCloud, where the bundle stays as signed, and passes the strict check
# before it is zipped.
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
