package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// errHelp is returned by parseArgs when -h/--help is encountered.
var errHelp = errors.New("help requested")

// FileSpec describes a file to tail with its display label.
type FileSpec struct {
	Path  string
	Label string
}

var rootCmd = &cobra.Command{
	Use:                "muxtail [flags] [--prefix=MODE] FILE [[--prefix=MODE] FILE ...]",
	Short:              "Tail multiple files with optional per-file prefixes",
	DisableFlagParsing: true,
	RunE:               run,
}

// resolveLabel returns the prefix string for a file given a mode.
func resolveLabel(path, mode string) string {
	if strings.HasPrefix(mode, "label:") {
		return strings.TrimPrefix(mode, "label:")
	}
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
	default: // "none", "", unrecognised
		return ""
	}
}

func isValidMode(mode string) bool {
	switch mode {
	case "none", "basename", "fullname", "":
		return true
	}
	return strings.HasPrefix(mode, "label:")
}

// parseArgs parses the raw argument list and returns file specs plus options.
func parseArgs(argv []string) (specs []FileSpec, n int, follow bool, retry bool, err error) {
	n = 10
	pendingMode := "none"
	lastWasPrefix := false
	dashdash := false

	for i := 0; i < len(argv); i++ {
		tok := argv[i]

		if !dashdash && tok == "--" {
			dashdash = true
			continue
		}

		if !dashdash && (tok == "--help" || tok == "-h") {
			return nil, 0, false, false, errHelp
		}

		if !dashdash {
			// --prefix=VALUE
			if strings.HasPrefix(tok, "--prefix=") {
				if lastWasPrefix {
					return nil, 0, false, false, fmt.Errorf("two --prefix in a row")
				}
				pendingMode = strings.TrimPrefix(tok, "--prefix=")
				lastWasPrefix = true
				continue
			}
			// -p=VALUE
			if strings.HasPrefix(tok, "-p=") {
				if lastWasPrefix {
					return nil, 0, false, false, fmt.Errorf("two --prefix in a row")
				}
				pendingMode = strings.TrimPrefix(tok, "-p=")
				lastWasPrefix = true
				continue
			}
			// --prefix VALUE or -p VALUE
			if tok == "--prefix" || tok == "-p" {
				if lastWasPrefix {
					return nil, 0, false, false, fmt.Errorf("two --prefix in a row")
				}
				i++
				if i >= len(argv) {
					return nil, 0, false, false, fmt.Errorf("%s requires a value", tok)
				}
				pendingMode = argv[i]
				lastWasPrefix = true
				continue
			}
			// -pVALUE (but not -p alone, handled above)
			if strings.HasPrefix(tok, "-p") && len(tok) > 2 {
				if lastWasPrefix {
					return nil, 0, false, false, fmt.Errorf("two --prefix in a row")
				}
				pendingMode = tok[2:]
				lastWasPrefix = true
				continue
			}

			// -n INT or --lines INT
			if tok == "-n" || tok == "--lines" {
				i++
				if i >= len(argv) {
					return nil, 0, false, false, fmt.Errorf("%s requires a value", tok)
				}
				v, parseErr := strconv.Atoi(argv[i])
				if parseErr != nil {
					return nil, 0, false, false, fmt.Errorf("invalid value for %s: %s", tok, argv[i])
				}
				n = v
				continue
			}
			// --lines=INT
			if strings.HasPrefix(tok, "--lines=") {
				val := strings.TrimPrefix(tok, "--lines=")
				v, parseErr := strconv.Atoi(val)
				if parseErr != nil {
					return nil, 0, false, false, fmt.Errorf("invalid value for --lines: %s", val)
				}
				n = v
				continue
			}
			// -nINT
			if strings.HasPrefix(tok, "-n") && len(tok) > 2 {
				val := tok[2:]
				v, parseErr := strconv.Atoi(val)
				if parseErr != nil {
					return nil, 0, false, false, fmt.Errorf("unknown flag: %s", tok)
				}
				n = v
				continue
			}

			// -f / --follow
			if tok == "-f" || tok == "--follow" {
				follow = true
				continue
			}

			// -F / --follow-retry
			if tok == "-F" || tok == "--follow-retry" {
				follow = true
				retry = true
				continue
			}

			// Unknown flags
			if strings.HasPrefix(tok, "-") {
				return nil, 0, false, false, fmt.Errorf("unknown flag: %s", tok)
			}
		}

		// File argument
		if !isValidMode(pendingMode) {
			return nil, 0, false, false, fmt.Errorf("invalid --prefix value %q: must be none, basename, fullname, or label:<text>", pendingMode)
		}
		specs = append(specs, FileSpec{Path: tok, Label: resolveLabel(tok, pendingMode)})
		lastWasPrefix = false
	}

	if lastWasPrefix {
		return nil, 0, false, false, fmt.Errorf("--prefix with no following file")
	}
	if len(specs) == 0 {
		specs = []FileSpec{{Path: "-", Label: ""}}
	}
	return specs, n, follow, retry, nil
}

func run(cmd *cobra.Command, args []string) error {
	specs, n, follow, retry, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		return cmd.Help()
	}
	if err != nil {
		return err
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
