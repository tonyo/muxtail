package main

import (
	"context"
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

var rootCmd = &cobra.Command{
	Use:   "muxtail [flags] FILE [FILE ...]",
	Short: "Tail multiple files with per-file labels",
	Args:  cobra.ArbitraryArgs,
	RunE:  run,
}

func init() {
	rootCmd.Flags().StringArrayP("label", "l", nil, "label for next file (repeatable, matched by position)")
	rootCmd.Flags().IntP("lines", "n", 10, "number of existing lines to show on startup")
	rootCmd.Flags().BoolP("follow", "f", false, "follow file for new lines")
	rootCmd.Flags().BoolP("followRetry", "F", false, "same as -f but retry if file is inaccessible")
}

func run(cmd *cobra.Command, args []string) error {
	labels, _ := cmd.Flags().GetStringArray("label")
	n, _ := cmd.Flags().GetInt("lines")
	follow, _ := cmd.Flags().GetBool("follow")
	retry, _ := cmd.Flags().GetBool("followRetry")
	if retry {
		follow = true
	}

	if len(args) == 0 {
		args = []string{"-"}
	}

	specs := make([]FileSpec, len(args))
	for i, f := range args {
		label := ""
		if i < len(labels) {
			label = labels[i]
		}
		if label == "" {
			if f == "-" {
				label = "stdin "
			} else {
				label = filepath.Base(f) + " "
			}
		}
		specs[i] = FileSpec{Path: f, Label: label}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	writer := &Writer{w: os.Stdout}

	errCh := make(chan error, len(specs))
	var wg sync.WaitGroup
	for _, spec := range specs {
		spec := spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			if spec.Path == "-" {
				tailStdin(ctx, spec.Label, writer)
			} else {
				errCh <- tailFile(ctx, spec, n, follow, retry, writer)
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
