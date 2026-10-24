# Budowa Base image

Decyzja: ADR-0006 (Packer CLI zarządzany przez `afk`). Budowa ma internet przez Egress proxy, ale bez sekretów i treści ticketów (ADR-0001).

## Packer

| | |
|---|---|
| Wersja | 1.16.2, tylko z releases.hashicorp.com (binarek nie ma na GitHubie) |
| Plik | `packer_1.16.2_linux_amd64.zip` |
| SHA256 | `abbfe0cf74985084112659b53e3565f264f70a20914c3b7c4f789f54f61c2414` |
| Plugin | `github.com/hashicorp/qemu` `= 1.1.7`, instalowany `packer init` |

Środowisko podprocesu:

```
PACKER_PLUGIN_PATH=~/.cache/afk/plugins
PACKER_CONFIG_DIR=~/.cache/afk/packer-config
CHECKPOINT_DISABLE=1
```

Wywołanie: `packer build -machine-readable -var-file=<gen>.json <embedded>.pkr.hcl`. `packer` z `PATH` jest ignorowany. Przerwanie → SIGINT, Packer sam sprząta QEMU.

**`-machine-readable`**: format `timestamp,target,type,data...`; przecinki w danych są zakodowane jako `%!(PACKER_COMMA)` — parser musi je odkodować po podziale na pola.

Przy podbiciu wersji: pobrać `SHA256SUMS` + `.sig`, `gpg --verify` kluczem HashiCorp `C874 011F 0AB4 0511 0D02 1055 3436 5D94 72D7 468F`, wpisać nową sumę do kodu.

## Wymagane klucze buildera

- `cpu_model = "host"` — z domyślnym `qemu64` Windows wpada w pętlę restartów.
- `accelerator = "kvm"`, `machine_type = "q35"`, `net_device = "virtio-net"`, `disk_interface = "virtio"`.
- `host_port_min = host_port_max = <ssh_port>` + `qemuargs` z `restrict=on` (patrz [isolation.md](isolation.md)) — `qemuargs` nadpisuje `-netdev` Packera, więc hostfwd trzeba podać samemu.
- `ssh_remote_tunnels = ["3128:127.0.0.1:<proxy_port>"]`.

## Template Debian 13

Źródło: `https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2` + `SHA512SUMS` (`iso_checksum = "file:..."`), `disk_image = true`. Konto i klucz przez cloud-init NoCloud: `cd_files = [user-data, meta-data]`, `cd_label = "cidata"`.

```hcl
packer {
  required_plugins {
    qemu = { version = "= 1.1.7", source = "github.com/hashicorp/qemu" }
  }
}

source "qemu" "debian" {
  iso_url              = var.base_image
  iso_checksum         = var.base_checksum
  disk_image           = true
  output_directory     = var.output_dir
  vm_name              = "debian13.qcow2"
  format               = "qcow2"
  disk_size            = "10G"
  accelerator          = "kvm"
  machine_type         = "q35"
  cpu_model            = "host"
  headless             = true
  memory               = 2048
  cpus                 = 2
  net_device           = "virtio-net"
  disk_interface       = "virtio"
  communicator         = "ssh"
  ssh_username         = "agent"
  ssh_private_key_file = var.ssh_key
  ssh_timeout          = "5m"
  host_port_min        = var.ssh_port
  host_port_max        = var.ssh_port
  ssh_remote_tunnels   = ["3128:127.0.0.1:${var.proxy_port}"]
  cd_files             = var.seed_files
  cd_label             = "cidata"
  shutdown_command     = "sudo shutdown -P now"
  qemuargs             = [["-netdev", "user,id=user.0,restrict=on,hostfwd=tcp:127.0.0.1:${var.ssh_port}-:22"]]
}

build {
  sources = ["source.qemu.debian"]
  provisioner "shell" { inline = var.provision }
}
```

Kroki końcowe (obowiązkowe, inaczej klony nie dostaną adresu albo dostaną ten sam):

```sh
sudo rm /etc/apt/apt.conf.d/90afk-proxy /etc/netplan/50-cloud-init.yaml
# netplan niezależny od nazwy karty z budowy
printf 'network:\n  version: 2\n  ethernets:\n    all:\n      match: {name: "en*"}\n      dhcp4: true\n' | sudo tee /etc/netplan/90-afk.yaml
sudo chmod 600 /etc/netplan/90-afk.yaml
sudo touch /etc/cloud/cloud-init.disabled   # klon nie odpala cloud-init
sudo truncate -s 0 /etc/machine-id          # unikalny machine-id = unikalny DHCP client-id
```

Czas budowy: 22–51 s.

## Po budowie

Artefakt (`*.qcow2`, dla UEFI `efivars.fd`) trafia do `/var/lib/afk/base/<name>/` z grupą `kvm`. Expiry z `[image].max_age`.
