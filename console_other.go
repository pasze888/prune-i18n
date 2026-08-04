//go:build !windows

package main

import "fmt"

func setupConsoleUTF8() {}

// readSecret 非 Windows 平台无法用标准库控制回显，直接读取。
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := readLine()
	fmt.Println()
	if err != nil {
		return "", err
	}
	return line, nil
}
