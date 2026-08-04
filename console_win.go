//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	stdInputHandle  = ^uintptr(10)
	enableEchoInput = 0x0004
)

// setupConsoleUTF8 将控制台代码页切换为 UTF-8，避免中文乱码（尽力而为）。
func setupConsoleUTF8() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	kernel32.NewProc("SetConsoleOutputCP").Call(65001)
	kernel32.NewProc("SetConsoleCP").Call(65001)
}

// readSecret 读取一行输入且不回显（用于 API Key），Windows 控制台专用。
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	if restore := disableEcho(); restore != nil {
		defer restore()
	}
	line, err := readLine()
	fmt.Println()
	if err != nil {
		return "", err
	}
	return line, nil
}

// disableEcho 关闭控制台回显，返回恢复函数；非控制台输入时返回 nil。
func disableEcho() func() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	h, _, _ := kernel32.NewProc("GetStdHandle").Call(stdInputHandle)
	var mode uint32
	if r, _, _ := kernel32.NewProc("GetConsoleMode").Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return nil
	}
	kernel32.NewProc("SetConsoleMode").Call(h, uintptr(mode&^enableEchoInput))
	return func() { kernel32.NewProc("SetConsoleMode").Call(h, uintptr(mode)) }
}
