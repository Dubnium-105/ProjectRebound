package matchlobby

import (
	"context"
	"testing"
)

func TestDedicatedNativeClearedRejectsUnscopedOwnedProcessEvidence(t *testing.T) {
	service := &Service{}
	_, err := service.DedicatedNativeCleared(
		context.Background(),
		"server-1",
		"authority-session-1",
		"attempt-1",
		"",
		1,
		1,
		OwnedProcessExitEvidence{
			EvidenceKind:            "pid_string",
			OwnedProcessID:          45123,
			ProcessStartFingerprint: "win-filetime:0123456789abcdef",
		},
	)
	if err == nil {
		t.Fatal("unscoped owned-process evidence was accepted")
	}
}
