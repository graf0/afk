package image

import (
	"slices"
	"strings"
	"testing"
)

func TestProvisionStepsOrder(t *testing.T) {
	steps := provisionSteps([]string{"project-step-1", "project-step-2"})

	index := func(needle string) (position int) {
		for i, step := range steps {
			if strings.Contains(step, needle) {
				return i
			}
		}
		return -1
	}
	proxyConf, packages, claude := index("Acquire::http::Proxy"), index("openssh-server"), index("claude.ai/install.sh")
	project1, project2 := index("project-step-1"), index("project-step-2")
	netplan, machineID := index("90-afk.yaml"), index("/etc/machine-id")

	for name, position := range map[string]int{
		"proxy conf": proxyConf, "packages": packages, "claude": claude,
		"project 1": project1, "project 2": project2, "netplan": netplan, "machine-id": machineID,
	} {
		if position < 0 {
			t.Fatalf("step %q missing from %q", name, steps)
		}
	}
	if !slices.IsSorted([]int{proxyConf, packages, claude, project1, project2, netplan, machineID}) {
		t.Errorf("wrong step order: %q", steps)
	}
	if machineID != len(steps)-1 {
		t.Errorf("machine-id reset must be the last step, steps: %q", steps)
	}
}

func TestProvisionStepsAlwaysInstallAfkRequirements(t *testing.T) {
	steps := strings.Join(provisionSteps(nil), "\n")
	for _, want := range []string{"openssh-server", "git", "claude.ai/install.sh", "cloud-init.disabled", `match: {name: "en*"}`, "rm -f /etc/apt/apt.conf.d/90afk-proxy"} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps lack %q", want)
		}
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
		if !strings.Contains(debian13Template, want) {
			t.Errorf("template lacks %q", want)
		}
	}
}
