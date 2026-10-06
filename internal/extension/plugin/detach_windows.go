package plugin

import "os/exec"

// detach leaves cmd as it is: on Windows, whether ssh may prompt is left
// to how the user set it up.
func detach(*exec.Cmd) {}
