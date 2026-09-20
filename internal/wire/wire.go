// Package wire is the language the desktop application and the on-phone agent
// share: the messages the agent streams back, and the plan it is given.
//
// Both sides import this package, so a change to the protocol cannot be made
// on one side alone.
package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"musiclibraryorganizer/internal/tags"
)

// Version identifies the protocol the two halves speak.
const Version = "1"

// Build identifies one agent binary by its contents.
//
// The protocol version alone is not enough: reading a new tag format changes
// the agent without changing the protocol, and an agent left on the phone by
// an earlier release would then keep failing on exactly the files the new one
// can read. Hashing the binary makes any change to it visible, so the phone
// never runs an older agent than the one it was handed.
func Build(binary []byte) string {
	sum := sha256.Sum256(binary)
	return Version + "-" + hex.EncodeToString(sum[:])[:16]
}

// SelfBuild is Build of the running executable, which is how the agent reports
// what it is when asked.
func SelfBuild() string {
	path, err := os.Executable()
	if err != nil {
		return Version + "-unknown"
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		return Version + "-unknown"
	}
	return Build(binary)
}

// Message types.
const (
	TypeStart   = "start"
	TypeTrack   = "track"
	TypeWritten = "written"
	TypeError   = "error"
	TypeDone    = "done"
)

// Message is one line of the agent's output stream. Exactly one of the payload
// fields is meaningful, according to Type.
type Message struct {
	Type string `json:"type"`

	Track *tags.Track `json:"track,omitempty"`
	Path  string      `json:"path,omitempty"`
	Error string      `json:"error,omitempty"`

	Done  int `json:"done"`
	Total int `json:"total"`
}

// Item is one file's pending edit, as written into the plan handed to the
// agent.
type Item struct {
	Path string    `json:"path"`
	Edit tags.Edit `json:"edit"`
}
