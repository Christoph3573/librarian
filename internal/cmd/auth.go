package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Christoph3573/librarian/internal/bsb"
	"github.com/Christoph3573/librarian/internal/session"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log in to BSB OPAC+ (login, status, logout)",
		Long: `Authenticate against https://opacplus.bsb-muenchen.de/discovery/login.

The login uses the BSB library card number (Ausweisnummer) + password via the
Primo VE PDS form (auth=local). The session JWT is stored in the user config
dir (mode 0600) and reused by research/inspect/borrow.

Credential sources (in order): --user/--password flags, BSB_USER/BSB_PASSWORD
env vars (also read from --env-file, e.g. a .env file), interactive prompt.

AGENT HINT: call 'librarian auth <subcommand> --help' to see flags. Searching
(research) also works anonymously without login; login is required for
request options, loans/requests and restricted downloads.`,
	}
	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	return cmd
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func newAuthLoginCmd() *cobra.Command {
	var user, password, envFile string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in with library card number + password",
		Long: `Log in to BSB OPAC+ and persist the session.

Examples:
  # interactive (prompts for password):
  librarian auth login

  # via .env file (BSB_USER=... BSB_PASSWORD=...):
  librarian auth login --env-file .env

  # via flags / env vars:
  librarian auth login --user 85007... --password '...'
  BSB_USER=... BSB_PASSWORD=... librarian auth login

The session is saved to the user config dir and shown with
'librarian auth status'. Use --json for machine-readable output.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if envFile != "" {
				loadEnvFile(envFile)
			} else if _, err := os.Stat(".env"); err == nil && os.Getenv("BSB_USER") == "" {
				loadEnvFile(".env") // convenience: pick up .env in cwd
			}
			if user == "" {
				user = os.Getenv("BSB_USER")
			}
			if password == "" {
				password = os.Getenv("BSB_PASSWORD")
			}
			if user == "" {
				fmt.Fprint(os.Stderr, "BSB library card number (Ausweisnummer): ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				user = strings.TrimSpace(line)
			}
			if password == "" {
				fmt.Fprint(os.Stderr, "Password: ")
				pw, err := term.ReadPassword(int(syscall.Stdin))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return fmt.Errorf("read password: %w", err)
				}
				password = string(pw)
			}
			if user == "" || password == "" {
				return fmt.Errorf("username and password are required")
			}
			client := bsb.NewClient(flagView, flagLang, "")
			token, loginID, err := client.Login(strings.TrimSpace(user), password)
			if err != nil {
				return err
			}
			claims, err := bsb.DecodeJWT(token)
			if err != nil {
				return err
			}
			sess := &session.Session{
				JWT:       token,
				LoginID:   loginID,
				User:      claims.User,
				UserName:  claims.UserName,
				Display:   claims.Display,
				ViewID:    claims.ViewID,
				ExpiresAt: time.Unix(claims.ExpiresAt, 0),
				CreatedAt: time.Now(),
			}
			if err := session.Save(sess); err != nil {
				return err
			}
			if flagJSON {
				return printJSON(sess)
			}
			fmt.Printf("Logged in as %s (%s)\n", claims.Display, claims.UserName)
			fmt.Printf("Session saved to %s (expires %s)\n", session.Path(), sess.ExpiresAt.Format(time.RFC1123))
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "library card number (or BSB_USER env)")
	cmd.Flags().StringVar(&password, "password", "", "password (or BSB_PASSWORD env)")
	cmd.Flags().StringVar(&envFile, "env-file", "", "read BSB_USER/BSB_PASSWORD from this dotenv file")
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current login session (+ loans/requests summary)",
		Long: `Show the saved login session and verify it against the server.

Without login, only anonymous/guest access is available (search works).
With login, loans and open requests are listed too.

Examples:
  librarian auth status
  librarian auth status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sess, err := session.Load()
			if err != nil {
				if flagJSON {
					return printJSON(map[string]any{"logged_in": false})
				}
				fmt.Println("Not logged in (anonymous/guest mode). Run `librarian auth login`.")
				return nil
			}
			client := bsb.NewClient(flagView, flagLang, sess.JWT)
			out := map[string]any{
				"logged_in":    true,
				"display_name": sess.Display,
				"user":         sess.UserName,
				"expired":      sess.Expired(),
				"expires_at":   sess.ExpiresAt.Format(time.RFC3339),
			}
			var loanList []bsb.Loan
			var holds, bookings, copies []bsb.HoldRequest
			if !sess.Expired() {
				loanList, _ = client.AccountLoans()
				holds, bookings, copies, _ = client.AccountRequests()
			}
			out["loans"] = len(loanList)
			out["open_holds"] = len(holds)
			out["open_bookings"] = len(bookings)
			out["open_photocopies"] = len(copies)
			if flagJSON {
				return printJSON(out)
			}
			fmt.Printf("Logged in as %s (%s)\n", sess.Display, sess.UserName)
			if sess.Expired() {
				fmt.Printf("Session EXPIRED at %s — run `librarian auth login` again.\n", sess.ExpiresAt.Format(time.RFC1123))
				return nil
			}
			fmt.Printf("Session valid until %s\n", sess.ExpiresAt.Format(time.RFC1123))
			fmt.Printf("Loans: %d | Holds: %d | Bookings: %d | Doc-delivery: %d\n", len(loanList), len(holds), len(bookings), len(copies))
			for _, l := range loanList {
				fmt.Printf("  loan: %s — due %s\n", l.Title, l.DueDate)
			}
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Delete the saved login session",
		Long: `Delete the saved session (logout). Safe to call when not logged in.

Examples:
  librarian auth logout`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := session.Clear(); err != nil {
				return err
			}
			fmt.Println("Logged out (session deleted).")
			return nil
		},
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
