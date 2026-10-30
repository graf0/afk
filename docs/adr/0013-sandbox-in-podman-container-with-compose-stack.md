---
status: accepted
---

# Sandbox w kontenerze podman z projektem compose per Chat i Run

Zastępuje ADR-0001, 0004, 0005, 0006, 0007 i 0011.

Agent pracuje w kontenerze rootless podman, a nie w VM. Każdy Chat i każdy Run to osobny projekt compose: serwis Agenta plus stack dev Projectu (baza, serwer aplikacji, frontend), z którym Agent rozmawia po sieci compose. Narzędzia z poświadczeniami hosta (Forge, logi i restart stacku, VM testowe) są dostępne tylko przez socket `afk` (ADR-0012), który dostaje sam kontener Agenta. To model sprawdzony w `ai.py` w dfmx (ADR-0029 tamże). VM-per-Run dawała mocniejszą izolację, ale za cenę budowania Base image (Packer, sysprep Windows), wolnego startu i braku stacku dev obok Agenta. Agent nie mógł podnieść aplikacji i zobaczyć jej działania.

## Consequences

- Nie ma Egress proxy: kontener ma pełny dostęp do sieci. Świadomie godzimy się na ryzyko eksfiltracji kodu Projectu przez Agenta po prompt injection. Sekretów hosta Agent nie widzi.
- Compose i obrazy pochodzą z gałęzi bazowej (ADR-0010), nigdy z drzewa Agenta. `afk` nie buduje obrazów z drzewa Agenta.
- Stack opisują dwa pliki z gałęzi bazowej, oba obowiązkowe: `compose.yml` i `compose.afk.yml` (serwis `afk` to Sandbox, opcjonalny `afk-setup` to one-shot setup). `.afk.toml` nie konfiguruje Stacku.
- `afk` woła `docker-compose` v2 na sockecie rootless podmana, a nie podman-compose: obejścia jego braków (`run --no-build`, `--in-pod`, `--podman-run-args`) w `ai.py` odrzuciliśmy. Socket API podmana widzi tylko `afk` na hoście.
- VM testowe to istniejące domeny libvirt hosta, którymi Agent steruje wąskimi trasami socketu, a nie maszyny tworzone przez `afk`.
