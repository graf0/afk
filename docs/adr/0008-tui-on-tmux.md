# TUI na osobnym serwerze tmux, nie własny emulator terminala

TUI `afk` to sesja na osobnym serwerze `tmux -L afk` z konfiguracją wbudowaną w `afk`: lewy panel to sidebar w bubbletea, a każdy Chat i Factory to okno tmux z procesem `afk`, który trzyma połączenie ssh, Egress proxy i pty Agenta. Przełączanie to podmiana panelu głównego, skróty to bindy tmux na `M-…` bez prefiksu. Chaty i Factory muszą przeżyć zamknięcie terminala, a Egress proxy żyje w procesie `afk` (ADR-0001) — tmux daje ten trwały proces i `detach`/`attach` bez pisania demona. Odrzuciliśmy `afkd` z TUI w bubbletea i wbudowanym emulatorem VT (własny protokół TUI↔demon, zewnętrzny i eksperymentalny pakiet emulatora, błędy emulacji po naszej stronie) oraz TUI bez demona (sesje giną z terminalem).

## Consequences

tmux jest zależnością systemową hosta. Osobny serwer i własna konfiguracja chronią przed zagnieżdżeniem i konfliktem z tmux użytkownika. Wygląd i skróty ograniczają możliwości tmux. Skróty `M-…` nie mogą kolidować ze skrótami Claude Code (np. `M-p`). Crash procesu `afk` w oknie zrywa ssh i proxy danego Chat; VM zostaje, Agent wraca przez `claude --continue`.
