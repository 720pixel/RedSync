package tools

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestCmdContextHonorsCancellation(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd, err := CmdContext(ctx, "sleep", "10")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := cmd.Run(); err == nil {
		t.Fatal("canceled child succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("child ignored cancellation")
	}
}
