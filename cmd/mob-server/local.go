package main

import (
	"os/exec"
	"strings"
)

func localRun(cmd string) (string, error) {
	c := exec.Command("bash", "-c", cmd)
	out, err := c.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}
