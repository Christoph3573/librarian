package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openURL launches the system browser for target. It is only called with
// explicit user consent (borrow --open on an open-browser route) — never
// implicitly, never for downloads.
func openURL(target string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", target)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		c = exec.Command("xdg-open", target)
	}
	if err := c.Run(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
