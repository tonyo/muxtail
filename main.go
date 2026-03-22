package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// FileSpec describes a file to tail with its display label.
type FileSpec struct {
	Path  string
	Label string
}

var (
	flagLines      int
	flagFollow     bool
	flagRetry      bool
	flagPrefix     string
	flagLabels     []string
	flagTimestamps bool
	flagNoColor    bool
)

var ansiColors = []string{
	"\033[36m", // cyan
	"\033[32m", // green
	"\033[33m", // yellow
	"\033[35m", // magenta
	"\033[34m", // blue
	"\033[31m", // red
	"\033[96m", // bright cyan
	"\033[92m", // bright green
	"\033[93m", // bright yellow
	"\033[95m", // bright magenta
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func colorizeLabel(label, code string) string {
	if label == "" {
		return label
	}
	return code + label + "\033[0m"
}

var rootCmd = &cobra.Command{
	Use:   "muxtail [flags] [FILE ...]",
	Short: "Tail multiple files with optional prefixes",
	RunE:  run,
}

func init() {
	rootCmd.Flags().IntVarP(&flagLines, "lines", "n", 10, "initial lines to show")
	rootCmd.Flags().BoolVarP(&flagFollow, "follow", "f", false, "follow file for new lines")
	rootCmd.Flags().BoolVarP(&flagRetry, "follow-retry", "F", false, "follow, retry if file is missing")
	rootCmd.Flags().StringVarP(&flagPrefix, "prefix", "p", "none", "global prefix mode: none|basename|fullname")
	rootCmd.Flags().StringArrayVarP(&flagLabels, "label", "l", nil, "per-file label (repeatable, positional)")
	rootCmd.Flags().BoolVarP(&flagTimestamps, "ts", "T", false, "prepend each line with a timestamp")
	rootCmd.Flags().BoolVar(&flagNoColor, "no-color", false, "disable colored labels")
}

// resolveLabel returns the prefix string for a file given a mode.
func resolveLabel(path, mode string) string {
	switch mode {
	case "basename":
		if path == "-" {
			return "stdin: "
		}
		return filepath.Base(path) + ": "
	case "fullname":
		if path == "-" {
			return "stdin: "
		}
		return path + ": "
	default: // "none", ""
		return ""
	}
}

func isValidPrefixMode(mode string) bool {
	return mode == "none" || mode == "basename" || mode == "fullname" || mode == ""
}

// buildSpecs combines positional labels and prefix mode into FileSpecs.
func buildSpecs(args, labels []string, prefixMode string) ([]FileSpec, error) {
	if len(labels) > len(args) {
		return nil, fmt.Errorf("more --label flags (%d) than files (%d)", len(labels), len(args))
	}
	specs := make([]FileSpec, len(args))
	for i, path := range args {
		if i < len(labels) {
			specs[i] = FileSpec{Path: path, Label: labels[i]}
		} else {
			specs[i] = FileSpec{Path: path, Label: resolveLabel(path, prefixMode)}
		}
	}
	return specs, nil
}

func run(cmd *cobra.Command, args []string) error {
	if !isValidPrefixMode(flagPrefix) {
		return fmt.Errorf("invalid --prefix %q: must be none, basename, or fullname", flagPrefix)
	}
	if len(args) == 0 {
		args = []string{"-"}
	}
	specs, err := buildSpecs(args, flagLabels, flagPrefix)
	if err != nil {
		return err
	}

	if !flagNoColor && isTerminal(os.Stdout) {
		for i := range specs {
			specs[i].Label = colorizeLabel(specs[i].Label, ansiColors[i%len(ansiColors)])
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
	}()

	writer := &Writer{w: os.Stdout, timestamps: flagTimestamps}

	errCh := make(chan error, len(specs))
	var wg sync.WaitGroup
	for _, spec := range specs {
		spec := spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			if spec.Path == "-" {
				errCh <- tailStdin(ctx, os.Stdin, spec.Label, writer)
			} else {
				errCh <- tailFile(ctx, spec, flagLines, flagFollow || flagRetry, flagRetry, writer)
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
