package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
)

// ErrNoTerminal is returned when there is nothing to draw on. It is a distinct
// error because the remedy is different from every other failure: the user did
// not mis-configure anything, they piped a program that needs a terminal.
var ErrNoTerminal = errors.New("TUI 는 터미널이 필요합니다. 파이프나 스크립트에서는 `goalforge status` 를 쓰세요")

// Options configure one TUI session.
type Options struct {
	RefreshEvery time.Duration
	// Output and Input default to the real terminal; tests supply their own.
	Output io.Writer
	Input  io.Reader
}

// Run draws the interface until the user quits.
func Run(ctx context.Context, loader Loader, options Options) error {
	output := options.Output
	if output == nil {
		output = os.Stdout
	}
	input := options.Input
	if input == nil {
		input = os.Stdin
	}
	if file, ok := output.(*os.File); ok && !isatty.IsTerminal(file.Fd()) && !isatty.IsCygwinTerminal(file.Fd()) {
		return ErrNoTerminal
	}
	// Colour is an accent on a layout that reads without it, so honouring
	// NO_COLOR costs emphasis and nothing else.
	if os.Getenv("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	program := tea.NewProgram(New(loader, options.RefreshEvery),
		tea.WithContext(ctx), tea.WithAltScreen(), tea.WithOutput(output), tea.WithInput(input))
	if _, err := program.Run(); err != nil {
		if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("TUI: %w", err)
	}
	return nil
}
