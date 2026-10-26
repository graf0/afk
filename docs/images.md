# Budowa Base image

Decyzja: ADR-0006 (Packer CLI zarządzany przez `afk`). Budowa ma internet przez Egress proxy, ale bez sekretów i treści ticketów (ADR-0001).

## Packer

| | |
|---|---|
| Wersja | 1.16.2, tylko z releases.hashicorp.com (binarek nie ma na GitHubie) |
| Plik | `packer_1.16.2_linux_amd64.zip` |
| SHA256 zipa | `abbfe0cf74985084112659b53e3565f264f70a20914c3b7c4f789f54f61c2414` |
| SHA256 binarki `packer` | `6f64a1e9e71be8b3a2f0225ccf0d51b5446fbf79d9ad323e917a0126ab6ee38a` |
| Plugin | `github.com/hashicorp/qemu` `= 1.1.7`, instalowany `packer init` |

Środowisko podprocesu jest minimalne (nic z env hosta poza `PATH`, `HOME`, `TMPDIR` jeśli ustawiony — bez `GITHUB_TOKEN`, `PKR_VAR_*` itd.):

```
PATH, HOME, TMPDIR
PACKER_PLUGIN_PATH=~/.cache/afk/plugins
PACKER_CONFIG_DIR=~/.cache/afk/packer-config
CHECKPOINT_DISABLE=1
```

Wywołanie: `packer build -machine-readable -var-file=<gen>.json <embedded>.pkr.hcl`. `packer` z `PATH` jest ignorowany. Binarka z cache jest weryfikowana (SHA256) przy każdym użyciu; niezgodna jest usuwana i pobierana ponownie (zip weryfikowany sumą, potem binarka).

Przerwanie: pierwszy Ctrl-C → jeden SIGINT do Packera (sam sprząta QEMU); po 2 min, albo od razu po drugim Ctrl-C, `SIGKILL` na całą grupę procesów Packera (plugin, QEMU). Packer ma też `Pdeathsig=SIGKILL` (ginie z `afk`; plugin i QEMU tylko przy zabiciu grupy).

**`-machine-readable`**: format `timestamp,target,type,data...`; przecinki w danych są zakodowane jako `%!(PACKER_COMMA)` — parser musi je odkodować po podziale na pola.

Przy podbiciu wersji: pobrać `SHA256SUMS` + `.sig`, `gpg --verify` kluczem HashiCorp `C874 011F 0AB4 0511 0D02 1055 3436 5D94 72D7 468F`, wpisać nową sumę zipa do kodu, rozpakować `packer`, policzyć `sha256sum packer` i wpisać też sumę binarki (kod i ta tabela razem).

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
  disk_size            = "64G"            # qcow2 rzadki; cloud-init przy budowie rozciąga partycję
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
  provisioner "shell" { inline = var.provision }       # kroki afk + build maszyny i jej Tooli
  provisioner "shell" { inline = var.final_provision } # zawsze ostatni
}
```

Kroki końcowe (obowiązkowe, inaczej klony nie dostaną adresu albo dostaną ten sam) idą w osobnym, ostatnim provisionerze, więc `exit`/`exec` w `build` ich nie pominie:

```sh
sudo rm /etc/apt/apt.conf.d/90afk-proxy /etc/netplan/50-cloud-init.yaml
# netplan niezależny od nazwy karty z budowy
printf 'network:\n  version: 2\n  ethernets:\n    all:\n      match: {name: "en*"}\n      dhcp4: true\n' | sudo tee /etc/netplan/90-afk.yaml
sudo chmod 600 /etc/netplan/90-afk.yaml
sudo touch /etc/cloud/cloud-init.disabled   # klon nie odpala cloud-init
sudo truncate -s 0 /etc/machine-id          # unikalny machine-id = unikalny DHCP client-id
```

Klucze hosta sshd zostają w obrazie, wspólne dla klonów; Sandbox VM dostaje własny ed25519 przez poświadczenie systemd w SMBIOS (docs/libvirt.md).

Czas budowy: 22–51 s.

## Po budowie

Kroki budowy w kolejności: `build` maszyny, `build` jej Tooli (alfabetycznie po nazwie), kroki końcowe `afk`. Konfiguracja z gałęzi bazowej (ADR-0010), także w `afk image build`.

Artefakt (`*.qcow2`, dla UEFI `efivars.fd`) trafia do `/var/lib/afk/base/<project>/<maszyna>/<wersja>/` z grupą `kvm`, razem z hashem konfiguracji obrazu (plik `config-hash`, SHA-256; powstaje w `.build-*` przed `rename`, więc ręczne `afk image build` też daje aktualny obraz); katalogi nad nim mają grupę `kvm` i `2775`. Hash obejmuje Template wbudowany w `afk` (źródłowy obraz, szablon Packera, seed cloud-init) i cały skrypt gościa w kolejności: kroki początkowe `afk`, pluginy i marketplace'y (`[agent]`, `.claude/settings.json`), `build` maszyny, `build` Tooli i kroki końcowe `afk`; nie obejmuje `memory`, `cpus`, `max_age` (Expiry liczy się osobno, z wieku wersji) ani `env`, `prompt`, `setup` Tooli. Wersja to znacznik czasu UTC (`20261024T153000Z`, sortuje się leksykograficznie). Budowa idzie do `.build-*` w katalogu maszyny i jest przenoszona `rename`-em, więc wersja jest zawsze kompletna; po każdej udanej budowie (Factory i `afk image build`, pod tym samym lockiem per Project) znikają wersje maszyny, które nie są najnowsze, nie są poprzednią najnowszą i nie są backing file żadnego overlayu Runa ani Chata (`machine.Overlays`; backing file czytany z nagłówka qcow2, względny liczony od katalogu overlayu). Poprzednia najnowsza zostaje, bo Factory bierze wersję z `Ensure`, a overlay tworzy dopiero potem; zostaje znany sufit: proces zawieszony na dłużej niż dwie budowy (docelowo: tworzenie overlayu pod wspólnym lockiem); VM po porażce trzyma więc swoją wersję, dopóki jego overlay istnieje. Katalogi `.build-*` nie są ruszane, a błąd sprzątania to ostrzeżenie w jednej linii, nie błąd budowy. Sandbox VM bierze najnowszą wersję maszyny `sandbox` (`image.Latest(paths, project, machine)`); kiedy Factory przebudowuje: [workflow.md](workflow.md#base-image-w-factory).
