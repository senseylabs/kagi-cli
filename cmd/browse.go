package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/ui"
)

// BrowseNode is one folder or leaf in an interactive drill-down. Name is the
// bare slug/name; Path is the full node path used to descend into a folder or to
// address a leaf; Label is the primary display text in the picker (defaults to
// Name when empty); Secondary is dimmer supporting text (a URL, an expiry).
type BrowseNode struct {
	Name      string
	Path      string
	Label     string
	Secondary string
}

// runInteractiveBrowse runs a generic drill-down loop shared by the
// secrets/cert/passwords browse commands. Starting at start, it repeatedly lists
// the children at the current path, presents them through the ui picker (folders
// first, then leaves), and acts on the choice: descending into a folder, calling
// onLeaf for a leaf, going up a level, or quitting. The current path is shown as
// the picker title, and the go-up command is offered only below the root.
//
// listChildren returns the folders and leaves directly under path. onLeaf is
// invoked with a chosen leaf's path and node; its error aborts the loop.
// Quitting (or EOF) ends the loop and returns nil.
func runInteractiveBrowse(
	u *ui.UI,
	start string,
	listChildren func(path string) (folders []BrowseNode, leaves []BrowseNode, err error),
	onLeaf func(path string, leaf BrowseNode) error,
) error {
	current := start
	if strings.TrimSpace(current) == "" {
		current = "/"
	}

	for {
		folders, leaves, err := listChildren(current)
		if err != nil {
			return err
		}

		items := make([]ui.PickItem, 0, len(folders)+len(leaves))
		for _, f := range folders {
			items = append(items, browseItem(f, true))
		}
		for _, l := range leaves {
			items = append(items, browseItem(l, false))
		}

		res, err := u.Pick(current, items, ui.PickOptions{AllowUp: !isBrowseRoot(current)})
		if err != nil {
			return err
		}

		switch res.Kind {
		case ui.PickQuit:
			return nil
		case ui.PickGoUp:
			current = browseParent(current)
		case ui.PickSelected:
			node := res.Item.Value.(BrowseNode)
			if res.Item.IsFolder {
				current = node.Path
				continue
			}
			if err := onLeaf(node.Path, node); err != nil {
				return err
			}
		}
	}
}

// browseItem builds a picker item from a browse node. The label falls back to
// the node's Name when no explicit Label is set. The node itself rides along as
// the picker item's Value so the selection handler recovers the full node.
func browseItem(n BrowseNode, isFolder bool) ui.PickItem {
	label := n.Label
	if label == "" {
		label = n.Name
	}
	return ui.PickItem{
		Label:     label,
		Secondary: n.Secondary,
		IsFolder:  isFolder,
		Value:     n,
	}
}

// shouldBrowseInteractively reports whether a browse command with these args
// should drop into the interactive picker. It does so only for a bare invocation
// (no path argument) writing a human-facing table to a terminal — never when a
// path is given, when stdout is piped/redirected, or when -o json/yaml is set,
// so scripted and non-table paths keep their existing one-shot listing.
func shouldBrowseInteractively(cmd *cobra.Command, args []string) bool {
	if len(args) != 0 {
		return false
	}
	format, err := outputFormat()
	if err != nil || format != ui.FormatTable {
		return false
	}
	return newUI().IsTTY()
}

// isBrowseRoot reports whether path addresses the library root (empty or "/").
func isBrowseRoot(path string) bool {
	return strings.Trim(path, "/") == ""
}

// browseParent returns the parent node path of path, clamped at the root ("/").
func browseParent(path string) string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return "/"
	}
	segments := strings.Split(trimmed, "/")
	if len(segments) <= 1 {
		return "/"
	}
	return "/" + strings.Join(segments[:len(segments)-1], "/")
}
