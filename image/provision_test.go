package image

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestProvisionStepsOrder(t *testing.T) {
	provision, final := provisionSteps([]string{"claude plugin install p"}, []string{"project-step-1", "project-step-2"})

	index := func(needle string) (position int) {
		for i, step := range provision {
			if strings.Contains(step, needle) {
				return i
			}
		}
		return -1
	}
	proxyConf, packages, claude := index("Acquire::http::Proxy"), index("openssh-server"), index("claude.ai/install.sh")
	plugins, project1, project2 := index("claude plugin install p"), index("project-step-1"), index("project-step-2")
	for name, position := range map[string]int{
		"proxy conf": proxyConf, "packages": packages, "claude": claude, "plugins": plugins, "project 1": project1, "project 2": project2,
	} {
		if position < 0 {
			t.Fatalf("step %q missing from %q", name, provision)
		}
	}
	if !slices.IsSorted([]int{proxyConf, packages, claude, plugins, project1, project2}) {
		t.Errorf("wrong step order: %q", provision)
	}
	if project2 != len(provision)-1 {
		t.Errorf("project steps must end the first provisioner: %q", provision)
	}
	if !slices.Equal(final, finalSteps) {
		t.Errorf("final = %q, want %q", final, finalSteps)
	}
}

func TestProvisionStepsAlwaysInstallAfkRequirements(t *testing.T) {
	provision, final := provisionSteps(nil, nil)
	for _, want := range []string{"openssh-server", "git", "claude.ai/install.sh"} {
		if !strings.Contains(strings.Join(provision, "\n"), want) {
			t.Errorf("provision lacks %q", want)
		}
	}
	for _, want := range []string{"cloud-init.disabled", `match: {name: "en*"}`, "rm -f /etc/apt/apt.conf.d/90afk-proxy", "/etc/machine-id"} {
		if !strings.Contains(strings.Join(final, "\n"), want) {
			t.Errorf("final steps lack %q", want)
		}
	}
}

// The final steps must run in their own, last provisioner, so a project's
// `exit 0` or `exec` cannot skip them.
func TestFinalStepsAreTheLastProvisionerAndNotInProvision(t *testing.T) {
	provision, final := provisionSteps(nil, []string{"exit 0"})

	if !slices.Equal(final, finalSteps) {
		t.Errorf("final = %q", final)
	}
	for _, step := range provision {
		if slices.Contains(finalSteps, step) {
			t.Errorf("final step %q also in the project provisioner", step)
		}
	}

	hcl := templates["debian13"].HCL
	inlines := regexp.MustCompile(`(?m)^\s*inline\s*=\s*(var\.\w+)`).FindAllStringSubmatch(hcl, -1)
	if got := strings.Count(hcl, `provisioner "`); got != 2 || len(inlines) != 2 {
		t.Fatalf("provisioners = %d, inlines = %d, want 2 each", got, len(inlines))
	}
	if inlines[0][1] != "var.provision" || inlines[1][1] != "var.final_provision" {
		t.Errorf("inline sources in order = %v, want var.provision then var.final_provision", inlines)
	}
}

func TestRenderSeed(t *testing.T) {
	files, err := renderSeed("ssh-ed25519 AAAAKEY afk")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"meta-data", "user-data"}) {
		t.Fatalf("files = %v", names)
	}
	userData := files["user-data"]
	for _, want := range []string{"#cloud-config", "name: agent", "NOPASSWD:ALL", "- ssh-ed25519 AAAAKEY afk"} {
		if !strings.Contains(userData, want) {
			t.Errorf("user-data lacks %q:\n%s", want, userData)
		}
	}
	if !strings.HasPrefix(userData, "#cloud-config\n") {
		t.Error("user-data must start with #cloud-config")
	}
}

func TestTemplateKeepsIsolationKeys(t *testing.T) {
	for _, want := range []string{`restrict=on`, `cpu_model            = "host"`, `= 1.1.7`, `ssh_remote_tunnels`, `cd_label             = "cidata"`} {
		if !strings.Contains(templates["debian13"].HCL, want) {
			t.Errorf("template lacks %q", want)
		}
	}
}

func TestTemplatesAreComplete(t *testing.T) {
	for name, tmpl := range templates {
		if tmpl.Name != name || tmpl.ImageURL == "" || tmpl.ChecksumURL == "" || tmpl.HCL == "" || tmpl.DiskName == "" {
			t.Errorf("template %q incomplete: %+v", name, tmpl)
		}
		if tmpl.hclFile() != name+".pkr.hcl" {
			t.Errorf("hclFile = %q", tmpl.hclFile())
		}
	}
}
