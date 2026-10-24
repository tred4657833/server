package modules

import "os/exec"

func execLook(bin string) (string, error) { return exec.LookPath(bin) }
