package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

var passwordsCmd = &cobra.Command{
	Use:   "passwords [path]",
	Short: "Browse password folders and read passwords",
	Long: "Browse the passwords folder tree and read stored credentials.\n" +
		"  kagi passwords                                     browse the passwords root (folders + passwords)\n" +
		"  kagi passwords /sensey                             browse a folder\n" +
		"  kagi passwords list                                list every password (flat) with its folder path\n" +
		"  kagi passwords get /sensey/admin                   show a password by its node path\n" +
		"  kagi passwords reveal admin                        reveal by username, id, or /folder/username path\n\n" +
		"Passwords live inside password folders and carry no name — a credential is\n" +
		"addressed by its login username. A leading-slash argument is a node path\n" +
		"(folder segments then the username); anything else matches by username or id.\n" +
		"Passwords are never printed in browse or list output — use `reveal` for the value.",
	Args: cobra.MaximumNArgs(1),
	RunE: runPasswordBrowse,
}

var passwordsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all passwords with their folder paths",
	Args:  cobra.NoArgs,
	RunE:  runPasswordList,
}

var passwordsGetCmd = &cobra.Command{
	Use:               "get <USERNAME_OR_ID>",
	Short:             "Get password metadata (masked)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePasswordRefs,
	RunE:              runPasswordGet,
}

var passwordsRevealCmd = &cobra.Command{
	Use:               "reveal <USERNAME_OR_ID>",
	Short:             "Reveal the plaintext password value",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePasswordRefs,
	RunE:              runPasswordReveal,
}

var passwordsHistoryCmd = &cobra.Command{
	Use:               "history <USERNAME_OR_ID>",
	Short:             "Show password audit history",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePasswordRefs,
	RunE:              runPasswordHistory,
}

func init() {
	passwordsCmd.AddCommand(passwordsListCmd)
	passwordsCmd.AddCommand(passwordsGetCmd)
	passwordsCmd.AddCommand(passwordsRevealCmd)
	passwordsCmd.AddCommand(passwordsHistoryCmd)
	rootCmd.AddCommand(passwordsCmd)
}

// passwordPathEntry pairs a password leaf with its full node path (folder
// segments then the login username).
type passwordPathEntry struct {
	path string
	pw   client.PasswordListItem
}

// walkPasswordTree walks the password folder tree from path (inclusive of its
// leaf passwords), depth-first, appending every password it finds with its full
// node path to out. It mirrors how a password is addressed in the folder model:
// passwords live inside folders, so the path is the containing folder path plus
// the login username.
func walkPasswordTree(vc *client.KagiClient, path string, out *[]passwordPathEntry) error {
	pws, err := vc.ListPasswordsInFolder(path)
	if err != nil {
		return err
	}
	base := strings.TrimRight(path, "/")
	for _, p := range pws {
		*out = append(*out, passwordPathEntry{path: base + "/" + p.Username, pw: p})
	}

	children, err := vc.ListPasswordFolderChildren(path)
	if err != nil {
		return err
	}
	for _, f := range children.Folders {
		if err := walkPasswordTree(vc, base+"/"+f.Slug, out); err != nil {
			return err
		}
	}
	return nil
}

// findPassword resolves a non-path reference (username or id prefix) to a single
// password by matching against the flat password list. Passwords carry no name
// or slug, so a username is unique only WITHIN a folder — the flat list can hold
// several passwords sharing a username across different folders. Every match in a
// tier is collected and more than one is treated as ambiguous rather than
// silently taking the first.
func findPassword(vc *client.KagiClient, usernameOrID string) (*passwordPathEntry, error) {
	var entries []passwordPathEntry
	if err := walkPasswordTree(vc, "/", &entries); err != nil {
		return nil, err
	}

	matchTier := func(pred func(passwordPathEntry) bool) []int {
		var idx []int
		for i := range entries {
			if pred(entries[i]) {
				idx = append(idx, i)
			}
		}
		return idx
	}

	if m := matchTier(func(e passwordPathEntry) bool { return strings.EqualFold(e.pw.Username, usernameOrID) }); len(m) > 0 {
		return resolvePasswordMatches(entries, m, usernameOrID)
	}
	if m := matchTier(func(e passwordPathEntry) bool { return strings.HasPrefix(e.pw.ID, usernameOrID) }); len(m) > 0 {
		return resolvePasswordMatches(entries, m, usernameOrID)
	}
	return nil, fmt.Errorf("password %q not found", usernameOrID)
}

// resolvePasswordMatches turns a tier of flat-list matches into a single
// password. A lone match is returned directly. Multiple matches are ambiguous —
// usernames are unique only within a folder — so it errors, listing each
// candidate's full node path so the caller can re-run with an unambiguous
// /folder/username path.
func resolvePasswordMatches(entries []passwordPathEntry, idx []int, ref string) (*passwordPathEntry, error) {
	if len(idx) == 1 {
		return &entries[idx[0]], nil
	}

	candidates := make([]string, 0, len(idx))
	for _, i := range idx {
		candidates = append(candidates, entries[i].path)
	}
	sort.Strings(candidates)
	return nil, fmt.Errorf("password %q is ambiguous — it matches %d passwords:\n  %s\nUse the /folder/username node path to select one",
		ref, len(idx), strings.Join(candidates, "\n  "))
}

// resolvePasswordRef turns a CLI argument into a password id and its full node
// path. A leading-slash argument is a password node path, resolved through the
// resolve step (the machine path-to-id contract); anything else is matched by
// username or id prefix against the flat password list.
func resolvePasswordRef(vc *client.KagiClient, arg string) (id string, path string, err error) {
	if strings.HasPrefix(arg, "/") {
		resolved, err := vc.ResolvePassword(arg)
		if err != nil {
			return "", "", err
		}
		return resolved.PasswordID, "/" + strings.Trim(arg, "/"), nil
	}

	entry, err := findPassword(vc, arg)
	if err != nil {
		return "", "", err
	}
	return entry.pw.ID, entry.path, nil
}

// completePasswordRefs provides dynamic shell completion for a password
// reference argument, offering the full node path of every password (reusing the
// same tree walk that backs `passwords list`). It stays silent on any error so
// completion never blocks the shell.
func completePasswordRefs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := requireAuth(); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var entries []passwordPathEntry
	if err := walkPasswordTree(vc, "/", &entries); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	comps := make([]string, 0, len(entries))
	for _, e := range entries {
		comps = append(comps, e.path)
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

// passwordBrowseEntry is the per-node payload for `kagi passwords [path]` in
// json/yaml mode: a folder or password directly under the browsed path, with its
// full node path. The password value is never included.
type passwordBrowseEntry struct {
	Type     string `json:"type" yaml:"type"`
	Name     string `json:"name,omitempty" yaml:"name,omitempty"`
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
	URL      string `json:"url,omitempty" yaml:"url,omitempty"`
	Path     string `json:"path" yaml:"path"`
	ID       string `json:"id,omitempty" yaml:"id,omitempty"`
}

func runPasswordBrowse(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// A bare, terminal-bound, table-format invocation drops into the interactive
	// drill-down; everything else keeps the one-shot listing so scripts and
	// -o json/yaml behave predictably.
	if shouldBrowseInteractively(cmd, args) {
		return runInteractiveBrowse(u, "/", passwordBrowseChildren(vc), passwordBrowseLeaf(u, vc))
	}

	format, err := outputFormat()
	if err != nil {
		return err
	}

	path := "/"
	if len(args) == 1 {
		path = args[0]
	}

	children, err := vc.ListPasswordFolderChildren(path)
	if err != nil {
		return fmt.Errorf("failed to browse %q: %w", path, err)
	}
	pws, err := vc.ListPasswordsInFolder(path)
	if err != nil {
		return fmt.Errorf("failed to list passwords under %q: %w", path, err)
	}

	if len(children.Folders) == 0 && len(pws) == 0 {
		if format == ui.FormatTable {
			u.Info("No folders or passwords under %q.", path)
			return nil
		}
		return u.Print(format, []passwordBrowseEntry{}, nil)
	}

	base := strings.TrimRight(path, "/")
	// The wide columns — the URL, the node path and the id — are marked
	// truncatable so rows stay one line on a narrow terminal; the full values are
	// always available via -o json/yaml (piped output is never truncated).
	payload := make([]passwordBrowseEntry, 0, len(children.Folders)+len(pws))
	table := ui.NewTable("TYPE", "NAME", "USERNAME", "URL", "PATH", "ID")
	table.SetTruncatable(3, 0)
	table.SetTruncatable(4, 1)
	table.SetTruncatable(5, 2)
	for _, f := range children.Folders {
		payload = append(payload, passwordBrowseEntry{Type: "folder", Name: f.Name, Path: base + "/" + f.Slug})
		table.AddRow("folder", f.Name, "", "", base+"/"+f.Slug, "")
	}
	for _, p := range pws {
		payload = append(payload, passwordBrowseEntry{Type: "password", Username: p.Username, URL: p.URL, Path: base + "/" + p.Username, ID: p.ID})
		table.AddRow("password", "", p.Username, p.URL, base+"/"+p.Username, p.ID)
	}
	return u.Print(format, payload, table)
}

// passwordBrowseChildren adapts the folder/password listing to the generic
// interactive browse loop: folders become drill-down nodes, passwords become
// leaves labeled by their login username with the service URL as secondary.
func passwordBrowseChildren(vc *client.KagiClient) func(path string) (folders []BrowseNode, leaves []BrowseNode, err error) {
	return func(path string) ([]BrowseNode, []BrowseNode, error) {
		children, err := vc.ListPasswordFolderChildren(path)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to browse %q: %w", path, err)
		}
		pws, err := vc.ListPasswordsInFolder(path)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list passwords under %q: %w", path, err)
		}

		base := strings.TrimRight(path, "/")
		folders := make([]BrowseNode, 0, len(children.Folders))
		for _, f := range children.Folders {
			folders = append(folders, BrowseNode{Name: f.Name, Path: base + "/" + f.Slug, Label: f.Name})
		}
		leaves := make([]BrowseNode, 0, len(pws))
		for _, p := range pws {
			leaves = append(leaves, BrowseNode{Name: p.Username, Path: base + "/" + p.Username, Label: p.Username, Secondary: p.URL})
		}
		return folders, leaves, nil
	}
}

// passwordBrowseLeaf handles a password chosen in the interactive browse: it
// resolves the leaf's node path to an id and prints the masked detail table. The
// value is never revealed here — the drill-down is read-only metadata.
func passwordBrowseLeaf(u *ui.UI, vc *client.KagiClient) func(path string, leaf BrowseNode) error {
	return func(path string, leaf BrowseNode) error {
		resolved, err := vc.ResolvePassword(path)
		if err != nil {
			return err
		}
		detail, err := vc.GetPasswordDetail(resolved.PasswordID)
		if err != nil {
			return fmt.Errorf("failed to get password details: %w", err)
		}
		return printPasswordDetail(u, ui.FormatTable, detail, path)
	}
}

// passwordListEntry is the per-password payload for `passwords list` in
// json/yaml mode, pairing the password's flat metadata with the folder node path
// it lives in. The value is never included.
type passwordListEntry struct {
	Path     string `json:"path" yaml:"path"`
	Username string `json:"username" yaml:"username"`
	URL      string `json:"url" yaml:"url"`
	ID       string `json:"id" yaml:"id"`
}

func runPasswordList(cmd *cobra.Command, args []string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// Walk the password folder tree so every password is listed with the folder
	// path it lives in — the flat password list carries no path.
	var entries []passwordPathEntry
	if err := walkPasswordTree(vc, "/", &entries); err != nil {
		return fmt.Errorf("failed to list passwords: %w", err)
	}

	if len(entries) == 0 {
		if format == ui.FormatTable {
			u.Info("No passwords found.")
			return nil
		}
		return u.Print(format, []passwordListEntry{}, nil)
	}

	payload := make([]passwordListEntry, 0, len(entries))
	table := ui.NewTable("PATH", "USERNAME", "URL", "ID")
	table.SetTruncatable(2, 0)
	table.SetTruncatable(0, 1)
	table.SetTruncatable(3, 2)
	for _, e := range entries {
		payload = append(payload, passwordListEntry{
			Path:     e.path,
			Username: e.pw.Username,
			URL:      e.pw.URL,
			ID:       e.pw.ID,
		})
		table.AddRow(e.path, e.pw.Username, e.pw.URL, e.pw.ID)
	}
	return u.Print(format, payload, table)
}

// passwordGetPayload is the `passwords get` payload for json/yaml mode: the full
// masked password metadata, plus the folder node path it lives in.
type passwordGetPayload struct {
	client.PasswordListItem `json:",inline" yaml:",inline"`
	Path                    string `json:"path,omitempty" yaml:"path,omitempty"`
}

func runPasswordGet(cmd *cobra.Command, args []string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	passwordID, path, err := resolvePasswordRef(vc, args[0])
	if err != nil {
		return err
	}

	detail, err := vc.GetPasswordDetail(passwordID)
	if err != nil {
		return fmt.Errorf("failed to get password details: %w", err)
	}

	return printPasswordDetail(u, format, detail, path)
}

// printPasswordDetail renders a password's masked metadata. The password value
// is shown masked only — never in plaintext; `passwords reveal` is the sole path
// to the decrypted value.
func printPasswordDetail(u *ui.UI, format ui.Format, detail *client.PasswordListItem, path string) error {
	table := ui.NewTable("FIELD", "VALUE")
	table.SetTruncatable(1, 0)
	table.AddRow("Username", detail.Username)
	table.AddRow("URL", detail.URL)
	if path != "" {
		table.AddRow("Path", path)
	}
	table.AddRow("Password", detail.MaskedPassword)
	table.AddRow("Has Notes", fmt.Sprintf("%t", detail.HasNotes))
	table.AddRow("Has Linked TOTP", fmt.Sprintf("%t", detail.HasLinkedTOTP))
	if detail.LinkedTOTPLabel != "" {
		table.AddRow("Linked TOTP", detail.LinkedTOTPLabel)
	}
	table.AddRow("ID", detail.ID)
	table.AddRow("Created At", detail.CreatedAt)
	table.AddRow("Updated At", detail.UpdatedAt)

	payload := passwordGetPayload{PasswordListItem: *detail, Path: path}
	return u.Print(format, payload, table)
}

func runPasswordReveal(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	passwordID, _, err := resolvePasswordRef(vc, args[0])
	if err != nil {
		return err
	}

	revealed, err := vc.RevealPassword(passwordID)
	if err != nil {
		return fmt.Errorf("failed to reveal password: %w", err)
	}

	// The password value is the primary data — it stays on stdout as the first,
	// unlabelled line so it can be piped or redirected directly. The supporting
	// fields (username, notes, linked authenticator) follow as labeled data
	// lines only when present.
	u.Data(revealed.Password)
	if revealed.Username != "" {
		u.Dataf("username: %s\n", revealed.Username)
	}
	if revealed.URL != "" {
		u.Dataf("url: %s\n", revealed.URL)
	}
	if revealed.Notes != "" {
		u.Dataf("notes: %s\n", revealed.Notes)
	}
	if revealed.LinkedAuthenticatorItemID != "" {
		u.Dataf("authenticator: %s\n", revealed.LinkedAuthenticatorItemID)
	}
	return nil
}

func runPasswordHistory(cmd *cobra.Command, args []string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	passwordID, _, err := resolvePasswordRef(vc, args[0])
	if err != nil {
		return err
	}

	history, err := vc.GetPasswordHistory(passwordID)
	if err != nil {
		return fmt.Errorf("failed to get password history: %w", err)
	}

	if len(history) == 0 {
		if format == ui.FormatTable {
			u.Info("No history found.")
			return nil
		}
		return u.Print(format, []client.PasswordHistory{}, nil)
	}

	// The wide columns — the timestamp and the changed-by principal — are marked
	// truncatable so rows stay one line on a narrow terminal; the full values are
	// always available via -o json/yaml (piped output is never truncated).
	table := ui.NewTable("DATE", "CHANGE TYPE", "USERNAME", "CHANGED BY")
	table.SetTruncatable(3, 0)
	table.SetTruncatable(0, 1)
	for _, h := range history {
		table.AddRow(h.CreatedAt, h.ChangeType, h.Username, h.ChangedBy)
	}
	return u.Print(format, history, table)
}
