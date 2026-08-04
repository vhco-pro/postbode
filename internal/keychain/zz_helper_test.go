//go:build darwin

package keychain

import (
	"context"
	"os/exec"
)

func deleteForTest(ctx context.Context, account string) error {
	return exec.CommandContext(ctx, securityPath, "delete-generic-password",
		"-s", Service, "-a", account).Run()
}
