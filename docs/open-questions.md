# Otwarte kwestie

Niesprawdzone albo nierozstrzygnięte. Po rozstrzygnięciu: przenieść wynik do właściwego pliku, usunąć stąd.

## Obrazy Windows

- [ ] Budowa Windows przez Packer CLI (szablony jeszcze tylko przez bibliotekę). Provisioning natywnie: `powershell` + `windows-restart`, sysprep jako ostatni krok.
- [ ] Sysprep na Win11 — ryzyko blokady przez pakiety Appx zainstalowane per użytkownik (log `setupact.log` w `C:\Windows\System32\Sysprep\Panther`).
- [ ] Secure Boot z kluczami MS (`OVMF_VARS_4M.ms.fd`) zamiast `LabConfig\BypassSecureBootCheck`.
- [ ] Kompaktowanie obrazu (~13 GB): `sdelete -z`/`Optimize-Volume -ReTrim` + `qemu-img convert -c`?
- [ ] Usuwanie pliku odpowiedzi klona z hasłem po starcie (`C:\Windows\Panther\unattend.xml`).
- [ ] Nazwa komputera z `.afk.toml` w pliku odpowiedzi klona (per Run — wymaga wstrzyknięcia pliku przed startem albo zmiany nazwy po starcie).
- [ ] Autologon w klonie (`LogonCount`) — sterowanie ekranem wymaga zalogowanej sesji.

## Chat, Factory, TUI

- [ ] Limit równoczesnych VM: 31 GB RAM ≈ 8–10 maszyn z Chromium + Postgres. Odmowa nowej VM przy braku pamięci? Balloon dla bezczynnych Chat?
- [ ] Protokół `afk-forge` po kanale ssh (subsystem ssh? JSON na stdin/stdout?) i jak skille typu `to-tickets` mają go używać zamiast `gh`.
- [ ] Minimalna wersja tmux (bindy bez prefiksu, `swap-pane`/`join-pane`, konfiguracja przez `-f`).
- [ ] Czy skille `synced/` konta claude.ai synchronizują się same w VM przy tokenie z `setup-token` (jeśli tak — w Chat/Factory pojawią się mimo pominięcia przy kopiowaniu).
- [ ] Czy `--plugin-dir` uruchamia hooki pluginów (np. SessionStart) tak samo jak instalacja.
- [ ] Czy `M-…` dochodzi do tmux w popularnych terminalach (Alt jako Meta) i nie koliduje ze skrótami Claude Code.

## go-libvirt

- [ ] Zgłosić upstream: `DomainScreenshot` zwraca błąd XDR mimo kompletnego PNG.

## Sprzątanie hosta po spike'ach

- `~/.cache/afk-spike` (~37 GB): ISO, wejścia, artefakty.
- `/var/lib/afk/base/{debian13,ws2025,win11pro,ws2025-gen}` (~42 GB).
