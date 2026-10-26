# Otwarte kwestie

Niesprawdzone albo nierozstrzygnięte. Po rozstrzygnięciu: przenieść wynik do właściwego pliku, usunąć stąd.

## Obrazy

- [ ] `HTTP(S)_PROXY` hosta dla Packera (`packer init`, pobranie cloud image) — dziś Packer dostaje minimalne środowisko bez nich; dodać, gdy ktoś będzie za firmowym proxy.

## Sandbox VM

- [ ] Prywatny klucz hosta sshd VM widzą użytkownicy hosta z dostępem do libvirt lub `ps` (XML domeny, `-smbios type=11` w linii poleceń QEMU) — na razie akceptowane; alternatywa: zapis do overlayu przed startem.

- [ ] Wznowienie Run i `afk vm shell` biorą allowlistę z dzisiejszej gałęzi bazowej, nie z `.afk.toml` z bazy Run (Quarantine trzyma tylko commit). Po zmianie `[network].allow` między porażką a wznowieniem sieć różni się od tej, którą miał Agent — czytać config z `refs/afk/base/ticket-<n>`?

## Obrazy Windows

- [ ] Budowa Windows przez Packer CLI (szablony jeszcze tylko przez bibliotekę). Provisioning natywnie: `powershell` + `windows-restart`, sysprep jako ostatni krok.
- [ ] Sysprep na Win11 — ryzyko blokady przez pakiety Appx zainstalowane per użytkownik (log `setupact.log` w `C:\Windows\System32\Sysprep\Panther`).
- [ ] Secure Boot z kluczami MS (`OVMF_VARS_4M.ms.fd`) zamiast `LabConfig\BypassSecureBootCheck`.
- [ ] Kompaktowanie obrazu (~13 GB): `sdelete -z`/`Optimize-Volume -ReTrim` + `qemu-img convert -c`?
- [ ] Usuwanie pliku odpowiedzi klona z hasłem po starcie (`C:\Windows\Panther\unattend.xml`).
- [ ] Nazwa komputera z `.afk.toml` w pliku odpowiedzi klona (per Run — wymaga wstrzyknięcia pliku przed startem albo zmiany nazwy po starcie).
- [ ] Autologon w klonie (`LogonCount`) — sterowanie ekranem wymaga zalogowanej sesji.

## Chat, Factory, TUI

- [ ] Czy skille `synced/` konta claude.ai synchronizują się same w VM przy tokenie z `setup-token` (jeśli tak — pojawią się w Chat/Factory mimo ADR-0009).
- [ ] „Pack bomb” z VM: bundle wyniku ma limit 1 GiB, ale rozmiar po rozpakowaniu w Quarantine repo (delty, duże bloby) nie jest ograniczony — limit na obiekty (`fetch.unpackLimit`/`transfer` + kontrola rozmiaru), czy wystarczy kontrola miejsca na dysku?
- [ ] Czy `M-…` dochodzi do tmux w popularnych terminalach (Alt jako Meta; na macOS wymaga „Option as Meta”). Kolizje z Claude Code rozstrzygnięte w `workflow.md` (TUI).

## go-libvirt

- [ ] Zgłosić upstream: `DomainScreenshot` zwraca błąd XDR mimo kompletnego PNG.

## Sprzątanie hosta po spike'ach

- `~/.cache/afk-spike` (~37 GB): ISO, wejścia, artefakty.
- `/var/lib/afk/base/{debian13,ws2025,win11pro,ws2025-gen}` (~42 GB).
