package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"
)

var (
	green  = color.New(color.FgGreen).SprintFunc()
	red    = color.New(color.FgRed).SprintFunc()
	yellow = color.New(color.FgYellow).SprintFunc()
	cyan   = color.New(color.FgCyan).SprintFunc()
	bold   = color.New(color.Bold).SprintFunc()
)

func Info(format string, a ...any) {
	fmt.Printf("  %s %s\n", cyan("●"), fmt.Sprintf(format, a...))
}

func Ok(format string, a ...any) {
	fmt.Printf("  %s %s\n", green("✓"), fmt.Sprintf(format, a...))
}

func Warn(format string, a ...any) {
	fmt.Printf("  %s %s\n", yellow("!"), fmt.Sprintf(format, a...))
}

func Fail(format string, a ...any) {
	fmt.Printf("  %s %s\n", red("✗"), fmt.Sprintf(format, a...))
}

func Fatal(format string, a ...any) {
	Fail(format, a...)
	os.Exit(1)
}

func Header(text string) {
	fmt.Printf("\n%s\n", bold(text))
}

func Step(n, total int, msg string) *spinner.Spinner {
	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	s.Suffix = fmt.Sprintf(" [%d/%d] %s", n, total, msg)
	s.Start()
	return s
}

func StepDone(s *spinner.Spinner, msg string) {
	s.Stop()
	Ok(msg)
}

func StepFail(s *spinner.Spinner, msg string) {
	s.Stop()
	Fail(msg)
}
