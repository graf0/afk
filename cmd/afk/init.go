package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"
	"github.com/pelletier/go-toml/v2"

	"github.com/graf0/afk/config"
	"github.com/graf0/afk/stack"
)

// configExample follows the generated .afk.toml: the optional sections, commented out.
const configExample = `
# Optional sections, see the .afk.toml section of README.md:
#
# [agent]
# model = "opus"
# plugins = ["mattpocock-skills@claude-plugins-official"]
#
# [expose]
# web = "8080:5173"
#
# [[vm]]
# name = "win11"
# user = "admin"
# os = "windows"
#
# [protect]
# paths = ["scripts/deploy/"]
`

// --- command ---

// runInit writes .afk.toml, asking for the forge and the gate, and the Stack
// template into the working tree containing dir. Existing files are kept.
// Nothing is committed.
func runInit(ctx context.Context, dir string, stdout io.Writer) (err error) {
	root, err := config.FindRoot(dir)
	if err != nil {
		return
	}
	var created []string
	path := filepath.Join(root, config.FileName)
	if _, statErr := os.Lstat(path); statErr == nil {
		_, _ = fmt.Fprintf(stdout, "%s exists and is not overwritten; check it with `afk config`\n", path)
	} else {
		if err = writeConfig(ctx, root, path); err != nil {
			return
		}
		created = append(created, config.FileName)
	}
	switch existing := stack.ExistingTemplateFiles(root); len(existing) {
	case 0:
		if err = stack.WriteTemplate(root); err != nil {
			return
		}
		created = append(created, "compose.afk.yml", ".afk")
	default:
		_, _ = fmt.Fprintf(stdout, "%s exists and is not overwritten\n", strings.Join(existing, " and "))
	}
	if len(created) == 0 {
		return
	}
	_, _ = fmt.Fprintf(stdout, "created %s in %s. afk reads them from the base branch and does not commit them: review, then\n  git add %s && git commit -m 'afk: init' && git push\n",
		strings.Join(created, ", "), root, strings.Join(created, " "))
	return
}

// writeConfig asks for the forge and the gate on the terminal and writes
// path, a new .afk.toml, if the answers make a valid config.
func writeConfig(ctx context.Context, root, path string) (err error) {
	if !isTerminal(os.Stdin) {
		err = fmt.Errorf("run `afk init` on a terminal, or write %s yourself (see README.md)", path)
		return
	}
	forge, _ := parseRemote(gitOrigin(ctx, root))
	if forge.Kind == "" {
		forge.Kind = config.ForgeGitHub
	}
	var gateCmd string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Forge").Options(huh.NewOptions(config.ForgeGitHub, config.ForgeGitea)...).Value(&forge.Kind),
			huh.NewInput().Title("Repository (owner/name)").Value(&forge.Repo).Validate(required),
		),
		huh.NewGroup(
			huh.NewInput().Title("Gitea URL (https://...)").Value(&forge.URL).Validate(required),
		).WithHideFunc(func() bool { return forge.Kind != config.ForgeGitea }),
		huh.NewGroup(
			huh.NewInput().Title("Gate: the command a change must pass").Placeholder("go test ./...").Value(&gateCmd).Validate(required),
		),
	)
	if err = form.RunWithContext(ctx); err != nil {
		err = fmt.Errorf("asking for %s: %w", config.FileName, err)
		return
	}
	if forge.Kind == config.ForgeGitHub {
		forge.URL = ""
	}
	data, err := renderConfig(forge, gateCmd)
	if err != nil {
		return
	}
	if _, err = config.ParseNamed(config.FileName, data); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, err = file.Write(data)
	err = errors.Join(err, file.Close())
	return
}

// required is a form field validator: the value must not be blank.
func required(s string) (err error) {
	if strings.TrimSpace(s) == "" {
		err = errors.New("required")
	}
	return
}

// --- origin ---

// gitOrigin is the URL of the remote origin of the repository at root, or "".
func gitOrigin(ctx context.Context, root string) (remote string) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "origin").Output()
	if err == nil {
		remote = strings.TrimSpace(string(out))
	}
	return
}

// parseRemote guesses the forge from a git remote URL: https://host/o/r,
// ssh://git@host:port/o/r or git@host:o/r, each with an optional .git.
// github.com is GitHub; any other host is taken for Gitea at https://host.
// ponytail: Gitea under a subpath (https://host/gitea/o/r) is not detected; the user fixes it in the form.
func parseRemote(remote string) (forge config.Forge, ok bool) {
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at, rest, found := strings.Cut(remote, "@"); found && !strings.Contains(at, "/") {
		host, path, _ = strings.Cut(rest, ":")
	}
	repo := strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || strings.Count(repo, "/") != 1 {
		return
	}
	forge = config.Forge{Kind: config.ForgeGitea, Repo: repo, URL: "https://" + host}
	if host == "github.com" {
		forge.Kind, forge.URL = config.ForgeGitHub, ""
	}
	ok = true
	return
}

// --- rendering ---

// renderConfig is a .afk.toml with forge and the gate cmd, followed by configExample.
func renderConfig(forge config.Forge, gateCmd string) (data []byte, err error) {
	file := struct {
		Forge struct {
			Kind string `toml:"kind"`
			Repo string `toml:"repo"`
			URL  string `toml:"url,omitempty"`
		} `toml:"forge"`
		Gate struct {
			Cmd string `toml:"cmd"`
		} `toml:"gate"`
	}{}
	file.Forge.Kind, file.Forge.Repo, file.Forge.URL = forge.Kind, strings.TrimSpace(forge.Repo), strings.TrimSpace(forge.URL)
	file.Gate.Cmd = gateCmd
	if data, err = toml.Marshal(file); err != nil {
		err = fmt.Errorf("encoding %s: %w", config.FileName, err)
		return
	}
	data = append(data, configExample...)
	return
}
