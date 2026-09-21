#!/bin/sh
# Builds the macOS application.
#
#   ./build.sh        build/bin/MusicLibraryOrganizer.app
#   ./build.sh zip    the same, plus the zip for a release
set -e
cd "$(dirname "$0")"

PATH="$(go env GOPATH)/bin:$PATH"
export PATH

if ! command -v wails >/dev/null 2>&1; then
	echo "Installing the Wails CLI..."
	go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
fi

if [ "$1" = "zip" ]; then
	make mac-zip
else
	make mac
fi
