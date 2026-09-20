package device

// The agent that runs on the phone travels inside this application, so nothing
// has to be downloaded and the two halves can never fall out of step.

import (
	_ "embed"
	"errors"
	"strings"
)

//go:embed agent/mlm-agent-arm64
var agentARM64 []byte

// agentFor picks the build matching the phone's processor. Every Android phone
// that can still install applications is 64-bit, so that is the only build
// carried; anything else gets a clear refusal rather than a crash.
func agentFor(abi string) ([]byte, error) {
	abi = strings.ToLower(strings.TrimSpace(abi))
	if strings.HasPrefix(abi, "arm64") || strings.Contains(abi, "aarch64") {
		return agentARM64, nil
	}
	return nil, errors.New(abi)
}
