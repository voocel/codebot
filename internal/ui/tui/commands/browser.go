package commands

import (
	"os/exec"
	"runtime"
)

// openBrowser opens url in the user's browser. Failing is fine: the caller
// shows the URL too, for terminals with no browser at hand, as over SSH.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}
