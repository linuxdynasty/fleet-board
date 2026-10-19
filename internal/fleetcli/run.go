// Package fleetcli implements the local fleet command line.
package fleetcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// Input holds the invocation's streams and environment boundary.
type Input struct {
	Stdin     *os.File
	Stdout    io.Writer
	Stderr    io.Writer
	LookupEnv func(string) (string, bool)
	Version   string
	// NetworkBudget overrides the default only for callers of this internal API.
	// cmd/fleet leaves it unset so every invocation gets 30 seconds.
	NetworkBudget time.Duration
}

type invocation struct {
	in    Input
	flags listFlags
	json  bool
	id    string
	work  workKeyFlags
}

type listFlags struct {
	size                 int
	token, filter, order string
}

type workKeyFlags struct {
	kind, project, key, integration, provider string
}

var (
	errUsage   = errors.New("bad command usage")
	errToken   = errors.New("API tokens are unavailable in this build")
	errCommand = errors.New("command failed")
	errOutput  = errors.New("output write failed")
)

// Run executes one CLI command. All output is buffered until its requests
// succeed, so a later RPC failure cannot leave a partial document on stdout.
func Run(ctx context.Context, args []string, in Input) int {
	if in.Stdout == nil {
		in.Stdout = io.Discard
	}
	if in.Stderr == nil {
		in.Stderr = io.Discard
	}
	if in.LookupEnv == nil {
		in.LookupEnv = os.LookupEnv
	}
	if in.Version == "" {
		in.Version = "dev"
	}
	if err := checkDuplicateFlags(args); err != nil {
		_, _ = io.WriteString(in.Stderr, "fleet: invalid or repeated flag\n")
		return 2
	}
	inv := &invocation{in: in}
	root := inv.command()
	root.SetArgs(args)
	root.SetOut(in.Stdout)
	root.SetErr(in.Stderr)
	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.ExecuteContext(ctx); err != nil {
		if !errors.Is(err, errCommand) {
			if errors.Is(err, errToken) {
				_, _ = io.WriteString(in.Stderr, "fleet: API tokens are unavailable in this build\n")
			} else {
				_, _ = io.WriteString(in.Stderr, "fleet: invalid command or arguments; run fleet --help\n")
			}
			return 2
		}
		_, _ = io.WriteString(in.Stderr, "fleet: "+safeError(err)+"\n")
		return 1
	}
	return 0
}

func (i *invocation) command() *cobra.Command {
	root := &cobra.Command{
		Use: "fleet", Short: "Read the local fleet", Version: i.in.Version,
		Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(i.in.Stdout, "fleet %s\n", i.in.Version)
			if err != nil {
				return fmt.Errorf("%w: %w", errCommand, err)
			}
			return nil
		},
	}
	root.SetVersionTemplate("fleet {{.Version}}\n")
	root.SetFlagErrorFunc(func(*cobra.Command, error) error { return errUsage })
	root.CompletionOptions.DisableDefaultCmd = true
	for _, name := range []string{"status", "doctor", "machines", "accounts", "goals", "work"} {
		name := name
		c := &cobra.Command{Use: name, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			if name == "machines" || name == "accounts" || name == "goals" || name == "work" {
				if err := i.validateList(c.Flags().Changed("page-size")); err != nil {
					return err
				}
			}
			return i.execute(c.Context(), name, false)
		}}
		c.Flags().BoolVar(&i.json, "json", false, "print the full response as JSON")
		if name == "machines" || name == "accounts" || name == "goals" || name == "work" {
			c.Flags().IntVar(&i.flags.size, "page-size", 0, "rows in one page (1 to 500)")
			c.Flags().StringVar(&i.flags.token, "page-token", "", "opaque next-page token from the previous response")
			c.Flags().StringVar(&i.flags.filter, "filter", "", "server filter")
			c.Flags().StringVar(&i.flags.order, "order-by", "", "server order")
			show := &cobra.Command{Use: "show", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
				if name == "work" {
					if !i.validWorkKey(c) {
						return errUsage
					}
				} else {
					i.id = argv[0]
					if i.id == "" || !validText(i.id, 1024) {
						return errUsage
					}
				}
				return i.execute(c.Context(), name, true)
			}}
			if name == "work" {
				show.Args = cobra.NoArgs
				show.Flags().StringVar(&i.work.kind, "kind", "", "work kind")
				show.Flags().StringVar(&i.work.project, "project", "", "provider project")
				show.Flags().StringVar(&i.work.key, "key", "", "provider key")
				show.Flags().StringVar(&i.work.integration, "integration", "", "integration ID")
				show.Flags().StringVar(&i.work.provider, "provider", "", "provider name")
			}
			show.Flags().BoolVar(&i.json, "json", false, "print the full response as JSON")
			c.AddCommand(show)
		}
		root.AddCommand(c)
	}
	return root
}

func (i *invocation) validateList(sizeSet bool) error {
	if i.flags.size < 0 || i.flags.size > 500 || (i.flags.size == 0 && sizeSet) {
		return errUsage
	}
	for _, s := range []string{i.flags.token, i.flags.filter, i.flags.order} {
		if !validText(s, 4096) {
			return errUsage
		}
	}
	return nil
}

func validText(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func checkDuplicateFlags(args []string) error {
	seen := make(map[string]bool)
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if name == "" || seen[name] {
			return errUsage
		}
		seen[name] = true
	}
	return nil
}

func (i *invocation) execute(ctx context.Context, name string, show bool) error {
	if _, exists := i.in.LookupEnv("FLEET_TOKEN"); exists {
		return errToken
	}
	rawURL, exists := i.in.LookupEnv("FLEET_URL")
	if !exists {
		rawURL = "http://127.0.0.1:7080"
	}
	client, closeClient, err := newClient(rawURL)
	if err != nil {
		return errUsage
	}
	defer closeClient()
	if name != "doctor" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, i.networkBudget())
		defer cancel()
	}
	data, err := i.read(ctx, client, name, show)
	if err != nil {
		return fmt.Errorf("%w: %w", errCommand, err)
	}
	n, err := i.in.Stdout.Write(data)
	if err != nil || n != len(data) {
		return fmt.Errorf("%w: %w", errCommand, errOutput)
	}
	return nil
}

func (i *invocation) networkBudget() time.Duration {
	if i.in.NetworkBudget > 0 {
		return i.in.NetworkBudget
	}
	return 30 * time.Second
}
