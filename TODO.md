# TODO

Kamienie milowe po M1 (`docs/architecture.md`) z wstępnym planem. M1 to tickety #7–#10 w GitHub Issues. Przed rozbiciem kamienia na tickety: grilling i aktualizacja docs/ADR. Otwarte kwestie techniczne: `docs/open-questions.md`.

## M2 — `.afk.toml`, Base image, Gitea

Cel: Project opisuje swoje maszyny i Toole (Postgres, Playwright w Sandbox VM); Base image sam się odświeża; Gitea jako drugi Forge. Decyzje: ADR-0010, ADR-0011, `README.md` (klucze), `docs/workflow.md`, `docs/images.md`.

Domknięcie M1:

- [ ] `.afk.toml` dla dfmx na `main`: `[network].allow` dla Go (`proxy.golang.org`, `sum.golang.org`), `[machine.sandbox].build` z toolchainem, `[gate].cmd`. Warunek zastąpienia `ralph.sh`.
- [x] Odmowy Egress proxy (host, port, powód) do `<log>-egress.txt`, podsumowanie na koniec Run; ścieżka logu wypisywana na starcie Run.
- [x] `disk_size = "64G"` w Template Debian (partycja w gościu dziś 10G).
- [ ] `afk vm shell` dla VM po porażce: Egress proxy z tą samą polityką co Run.

`.afk.toml`:

- [x] `[image]` → `[machine.<nazwa>]` (`template`, `build`, `max_age` domyślnie `168h`, `memory`, `cpus`); `sandbox` zastrzeżone, inne nazwy odrzucane do M4.
- [ ] `[tool.<nazwa>]`: `on` (tylko `sandbox`), `build`, `setup`, `env`, `prompt`; usunięcie `kind`.
- [ ] Walidacja wszystkich sekcji (lista w `README.md`), wszystkie błędy naraz z `linia:kolumna`; `afk config` z drzewa roboczego.
- [ ] Toole w Run: `setup` po klonie (alfabetycznie, `<log>-setup.txt`, pomijany przy wznowieniu), `env` w sesjach i bramce, sekcja „Narzędzia” w promptach.

Base image:

- [ ] Konfiguracja z gałęzi bazowej także w `afk image build` i `afk vm up` (ADR-0010).
- [x] Base image w katalogu per maszyna `/var/lib/afk/base/<project>/<maszyna>/<wersja>/`.
- [x] Hash konfiguracji obrazu w katalogu wersji.
- [x] Factory buduje obraz przed Run (brak / Expiry / inny hash) pod `flock` per Project, log `image-<maszyna>-<wersja>.log`.
- [ ] Sprzątanie po udanej budowie: wersje nie najnowsze, niebędące backing file overlayu w `runs/` ani `chats/`.

Forge:

- [ ] Label `[labels].spec` definiuje Spec (GitHub i Gitea); usunięcie heurystyki `## User Stories`; ticket z sub-issues bez labela pomijany z ostrzeżeniem.
- [ ] Preflight: wszystkie labele z `[labels]` istnieją w repo.
- [ ] Gitea (REST) za interfejsem `Forge`: dzieci Spec z listy zadań `- [ ] #M`, blokery = natywne zależności + `Blocked by:`, token `GITEA_TOKEN` / `~/.local/state/afk/gitea-token`, push i PR.

## M3 — Chat, TUI, `afk-forge`

Cel: interaktywna praca z Agentem w VM i Factory z jednego okna (ADR-0008, `docs/workflow.md`).

- [ ] Chat: długo żyjąca Sandbox VM, kod jako snapshot gałęzi bazowej, Claude Code interaktywnie na pty (`--dangerously-skip-permissions`, `--append-system-prompt`), pełny internet przez Egress proxy (ADR-0003).
- [ ] Powrót Chat po restarcie hosta / crashu `afk`: VM z tego samego overlayu, `claude --continue`.
- [ ] Eksport Chat: commity przez Quarantine repo (ta sama walidacja co Run) na branch `chat-<nazwa>`, bez PR.
- [ ] `afk-forge` w VM (`issue create|view|list|comment`) przez kanał ssh do `afk`; `ready-for-agent` z Chat → `needs-triage`. Najpierw rozstrzygnąć protokół (open-questions).
- [ ] TUI: serwer `tmux -L afk` z wbudowaną konfiguracją, sidebar w bubbletea (Chaty i Factory per Project z metadanych libvirt), skróty `M-↑/↓`, `M-n`, `M-f`, `M-x`, `M-e`, `M-s`.
- [ ] Widok Factory: strumień Run tylko do odczytu, nagłówek z ticketem, wiekiem Run i czasem od ostatniego zdarzenia.
- [ ] Limit równoczesnych VM / pamięci (open-questions).

## M4 — Windows, Tool VM, sterowanie ekranem

Cel: Projekty wymagające Windowsa i GUI (spike'i w `docs/windows-images.md`, ADR-0005, ADR-0007).

- [ ] Template Windows (ws2025, win11) przez Packer CLI: ISO i SHA256 z `.afk.toml`, OpenSSH (Win11 z MSI), sysprep jako ostatni krok, UEFI + vTPM.
- [ ] Sandbox VM Windows: NVRAM per maszyna, `undefine --nvram --tpm`, klucz hosta sshd per VM (odpowiednik credentiala SMBIOS dla Windows), test izolacji z sondą PowerShell.
- [ ] Tool VM: `[machine.<nazwa>]` poza `sandbox`, Tool z `on = "<nazwa>"`, w sieci tego samego Run (filtr dopuszcza ruch w /24 — dziś jedna VM na sieć), podnoszona na żądanie Agenta przez kanał gość → `afk` (wspólny z `afk-forge`, ADR-0011), sprzątanie razem z Run.
- [ ] Sterowanie ekranem: `afk-screen` w VM przez tunel ssh, zrzuty PNG przez `DomainScreenshot` (obejście błędu XDR), wejście przez QMP `input-send-event`/`send-key`; autologon w klonie.
- [ ] Pozostałe punkty „Obrazy Windows” z open-questions.
