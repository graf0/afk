# TODO

Kamienie milowe po M1 (`docs/architecture.md`) z wstępnym planem. M1 to tickety #7–#10 w GitHub Issues. Przed rozbiciem kamienia na tickety: grilling i aktualizacja docs/ADR. Otwarte kwestie techniczne: `docs/open-questions.md`.

## Odłożone z M1

- [ ] `.afk.toml` dla dfmx na `main`: `[network].allow` dla Go (`proxy.golang.org`, `sum.golang.org`), `[image].provision` z toolchainem, `[gate].cmd`. Warunek zastąpienia `ralph.sh`.
- [ ] Logowanie odmów Egress proxy (host, port, powód) do logu Run — dziś brakującą domenę widać tylko po zachowaniu Agenta.
- [ ] Partycja w gościu zostaje 10G mimo overlayu 64G (brak growpart, cloud-init wyłączony): `growpart`/`resize2fs` przy starcie albo większy dysk w obrazie.
- [ ] Hostname `afk` we wszystkich VM; RAM 2 GiB i 2 vCPU na sztywno — do `.afk.toml`?
- [ ] `afk vm shell` dla VM po porażce: Egress proxy z tą samą polityką co Run.

## M2 — `.afk.toml` i Tool

Cel: Project opisuje swoje zależności; Agent ma bazę danych itp. bez dostępu do hosta.

- [ ] Domknięcie `.afk.toml`: walidacja wszystkich sekcji, `afk config` z wyjaśnieniem błędów, dokumentacja kluczy w `docs/project-config.md`.
- [ ] Expiry (`[image].max_age`): przebudowa Base image po terminie przed startem Run/Chat; sprzątanie starych wersji w `/var/lib/afk/base/<project>/` (nieużywanych przez żadną VM).
- [ ] Przebudowa obrazu, gdy zmieniły się `[image]` albo listy pluginów (`[agent].plugins`, `marketplaces`, `.claude/settings.json` Project; ADR-0009) (hash konfiguracji w katalogu wersji).
- [ ] Tool `package`: pakiet instalowany w Base image (`provision` generowany z `[tool.*]`), np. Postgres jako usługa w gościu.
- [ ] Tool `host-port`: usługa na hoście wystawiona Agentowi przez tunel ssh `-R` na loopbacku gościa (jak Egress proxy), tylko wskazany port; bez otwierania hosta na sieć VM. Test izolacji: inne porty hosta dalej zablokowane.
- [ ] Gitea jako drugi Forge (REST): interfejs `Forge`, relacje w treści (`Part of #N`, `Blocked by: #N`).

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
- [ ] Tool `vm`: osobna Tool VM w sieci tego samego Run (filtr dopuszcza ruch w /24 — dziś jedna VM na sieć), start przed Agentem, sprzątanie razem z Run.
- [ ] Sterowanie ekranem: `afk-screen` w VM przez tunel ssh, zrzuty PNG przez `DomainScreenshot` (obejście błędu XDR), wejście przez QMP `input-send-event`/`send-key`; autologon w klonie.
- [ ] Pozostałe punkty „Obrazy Windows” z open-questions.
