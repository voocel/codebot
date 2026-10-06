package plugin

import "os/exec"

// detach leaves cmd as it is: on Windows, whether ssh may prompt is left
// to how the user set it up, and git's helpers outlive it cancelled.
func detach(*exec.Cmd) {}
