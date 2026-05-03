package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func promptLine(reader *bufio.Reader, label, def string) (string, error) {
	if def != "" {
		fmt.Printf("  ? %s [%s]: ", label, def)
	} else {
		fmt.Printf("  ? %s: ", label)
	}

	line, err := reader.ReadString('\n')
	if err != nil && !(err == io.EOF && line != "") {
		return "", err
	}

	value := strings.TrimSpace(line)
	if value == "" {
		return def, nil
	}
	return value, nil
}

func promptRequired(reader *bufio.Reader, label, def string) (string, error) {
	for {
		value, err := promptLine(reader, label, def)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Println("    required")
	}
}

func promptInt(reader *bufio.Reader, label string, def int) (int, error) {
	defaultText := ""
	if def > 0 {
		defaultText = strconv.Itoa(def)
	}

	for {
		value, err := promptLine(reader, label, defaultText)
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(value)
		if err == nil && n > 0 && n <= 65535 {
			return n, nil
		}
		fmt.Println("    enter a port between 1 and 65535")
	}
}

func confirmPrompt(reader *bufio.Reader, label string, def bool) (bool, error) {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}

	for {
		value, err := promptLine(reader, label+" ("+suffix+")", "")
		if err != nil {
			return false, err
		}
		if value == "" {
			return def, nil
		}
		switch strings.ToLower(value) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Println("    enter y or n")
		}
	}
}
