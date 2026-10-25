# Dokumentacja afk

Słownik: [`GLOSSARY.md`](../GLOSSARY.md). `.afk.toml`: [`README.md`](../README.md). Decyzje trudne do odwrócenia: [`adr/`](adr/).

| Plik | O czym |
|---|---|
| [workflow.md](workflow.md) | Factory (od ticketu do PR, raport agenta, needs-info, git), Chat, TUI, stan |
| [architecture.md](architecture.md) | Pakiety, interfejsy, zależności, kamienie milowe, ścieżki na dysku |
| [isolation.md](isolation.md) | Sieć Run i budowy, nwfilter, wyniki testów izolacji |
| [egress-proxy.md](egress-proxy.md) | Implementacja Egress proxy na kanale ssh |
| [libvirt.md](libvirt.md) | Domeny libvirt, go-libvirt, czasy startu, ekran i wejście |
| [images.md](images.md) | Budowa Base image Packerem CLI, Template Debian |
| [windows-images.md](windows-images.md) | Template Windows: instalacja, sysprep, OpenSSH, pułapki |
| [open-questions.md](open-questions.md) | Co jeszcze niesprawdzone |

Liczby i wyniki pochodzą ze spike'ów z 2026-10-24 na hoście: Debian 13, kernel 6.12, libvirt 11.3, QEMU 10.0.13, 12 rdzeni, 31 GB RAM.
