# Architektura

## Pakiety

Bez `internal/`:

| Pakiet | Odpowiedzialność |
|---|---|
| `cmd/afk` | CLI |
| `config/` | `.afk.toml` |
| `forge/` | GitHub, Gitea (REST) |
| `machine/` | Sandbox VM i Tool VM przez libvirt |
| `image/` | Template → Base image (Packer CLI); nie `template`, kolizja z `text/template` |
| `agent/` | Claude Code w VM: prompt, sesja ssh, raport |
| `egress/` | Egress proxy |
| `run/` | Orkiestracja Run |
| `factory/` | Pętla Factory: selektor, rezerwacje (`flock`), kolejne Run |
| `chat/` | Chat: VM, interaktywny Agent na pty, eksport brancha, obsługa `afk-forge` |
| `tui/` | Sidebar (bubbletea v2) i sterowanie serwerem `tmux -L afk` (ADR-0008) |
| `cmd/afk-forge` | CLI w VM: operacje na ticketach przez kanał ssh do `afk` |

Wąskie interfejsy `Machine`, `Agent`, `Forge`. Na razie po jednej implementacji, poza `Forge` (GitHub, Gitea).

Stan bez bazy danych: przynależność VM w `<metadata>` domeny libvirt, rezerwacje `flock`, logi w plikach (szczegóły: [workflow.md](workflow.md#stan)).

## Zależności zatwierdzone

- `github.com/digitalocean/go-libvirt` — przypięty commit
- `github.com/pelletier/go-toml/v2`
- `charm.land/bubbletea/v2` + bubbles, lipgloss
- `golang.org/x/crypto/ssh`

Packer nie jest zależnością Go — to binarka pobierana w czasie działania (ADR-0006). tmux — zależność systemowa hosta (ADR-0008).

## Kanał do VM

ssh wszędzie, także Windows (sshd + klucz `afk` w obrazie). vsock odrzucony (brak na Windows, kanał inicjowany z gościa). Sterowanie ekranem: CLI `afk-screen` w VM przez tunel ssh, zrzuty PNG. Tak samo `afk-forge` w Chat.

## Ścieżki

| Ścieżka | Zawartość |
|---|---|
| `/var/lib/afk/base/<name>/` | Base image (`*.qcow2`, dla UEFI `efivars.fd`), grupa `kvm` |
| `/var/lib/afk/runs/<run>/` | Overlay i NVRAM jednego Run, katalog `2775` |
| `/var/lib/afk/chats/<chat>/` | Overlay i NVRAM jednego Chat, jak wyżej |
| `~/.local/state/afk/claims/<project>/<n>` | Rezerwacje ticketów (`flock`) |
| `~/.local/state/afk/<project>/logs/` | Logi Run (stream-json) |
| `~/.cache/afk/bin/packer-<ver>` | Packer pobrany przez `afk` |
| `~/.cache/afk/plugins` | `PACKER_PLUGIN_PATH` |
| `~/.cache/afk/packer-config` | `PACKER_CONFIG_DIR` |

`/var/lib/afk`, nie `$HOME`: `libvirt-qemu` nie czyta katalogu domowego, AppArmor blokuje zapis poza ścieżkami dysków (ADR-0001). Cache obrazów per projekt. Uwaga: `/tmp` na hoście to tmpfs — nie na obrazy.

## Kamienie milowe

1. Debian + izolacja + proxy + git + Factory bez TUI (`afk factory '*'`) → zastępuje `ralph.sh` dla dfmx.
2. `.afk.toml`, Tool jako pakiet / port hosta.
3. Chat + TUI (tmux + sidebar w bubbletea), `afk-forge`.
4. Windows, Tool VM, sterowanie ekranem.
