package cli

import (
	"bytes"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"shepherd/internal/auth"
)

func init() {
	cmd := &cobra.Command{Use: "hash-password", Short: "Hash a password for use as auth.local_admin.password_hash", RunE: runHashPassword}
	cmd.Flags().Bool("password-stdin", false, "read password from stdin (for scripting)")
	rootCmd.AddCommand(cmd)
}

func runHashPassword(cmd *cobra.Command, _ []string) error {
	fromStdin, _ := cmd.Flags().GetBool("password-stdin") //nolint:errcheck
	var pw []byte
	var err error
	if fromStdin {
		buf := make([]byte, 1024)
		n, readErr := os.Stdin.Read(buf)
		if readErr != nil && n == 0 {
			return fmt.Errorf("reading password from stdin: %w", readErr)
		}
		pw = bytes.TrimRight(buf[:n], "\r\n")
	} else {
		fmt.Fprint(os.Stderr, "Enter password: ")
		pw, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("reading password: %w", err)
		}
	}
	if len(pw) == 0 {
		return fmt.Errorf("password must not be empty")
	}
	hash, err := auth.HashPassword(string(pw))
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	fmt.Println(hash)
	return nil
}
