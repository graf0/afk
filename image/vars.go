package image

import (
	"encoding/json"
	"fmt"
	"os"
)

// packerVars are the values of the embedded Packer template's variables.
type packerVars struct {
	BaseImage      string   `json:"base_image"`
	BaseChecksum   string   `json:"base_checksum"`
	DiskName       string   `json:"disk_name"`
	OutputDir      string   `json:"output_dir"`
	SSHKey         string   `json:"ssh_key"`
	SSHPort        int      `json:"ssh_port"`
	ProxyPort      int      `json:"proxy_port"`
	SeedFiles      []string `json:"seed_files"`
	Provision      []string `json:"provision"`
	FinalProvision []string `json:"final_provision"`
}

// writeVarFile writes vars as a Packer JSON -var-file. Nil lists become empty
// lists, because Packer rejects null for list variables.
func writeVarFile(path string, vars packerVars) (err error) {
	if vars.SeedFiles == nil {
		vars.SeedFiles = []string{}
	}
	if vars.Provision == nil {
		vars.Provision = []string{}
	}
	if vars.FinalProvision == nil {
		vars.FinalProvision = []string{}
	}
	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		err = fmt.Errorf("encoding var-file: %w", err)
		return
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		err = fmt.Errorf("writing var-file: %w", err)
	}
	return
}
