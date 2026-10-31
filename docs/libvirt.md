# VM testowe (libvirt)

libvirt służy już tylko do jednego: Agent w Chat steruje istniejącymi VM testowymi z `[[vm]]` (ADR-0013). `afk` ich nie tworzy, nie buduje i nie niszczy. Factory ich nie dostaje. Składnia `afk-forge vm`: [agents/issue-tracker.md](agents/issue-tracker.md).

## Host

- `virsh` w `PATH` i dostęp użytkownika do `qemu:///system` (`libvirt` w grupach). `afk` woła `virsh --connect qemu:///system …` jako argv, nigdy przez powłokę; nazwa domeny trafia do `virsh` dopiero po znalezieniu w `[[vm]]`, nazwa snapshotu po dopasowaniu `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`.
- Domena musi istnieć. Zwracany adres to dzierżawa DHCP libvirt (`domifaddr --source lease`); bez niej `address` w `vm list` jest pusty.
- Ekran: `virsh screenshot` (PNG). Wejście (`key`, `type`, `click`, `move`): komendy QMP budowane przez `afk` z walidowanych argumentów (klawisze z listy dozwolonych, tekst US-ASCII), nigdy przekazywane od Agenta. Piksele `click`/`move` są względem ostatniego zrzutu tej VM, więc zrzut jest wymagany wcześniej.

## Przygotowanie VM testowej

Robi to człowiek, poza `afk`. Sprawdzone na spike'ach Windows (dawny Base image):

- Domena: `q35`, KVM, `cpu mode='host-passthrough'` (z domyślnym `qemu64` Windows wpada w pętlę restartów), sieć libvirt z DHCP (adres z dzierżawy), karta wideo (dla `virsh screenshot`) i `<input type='tablet' bus='usb'/>`: `click`/`move` wysyłają osie absolutne, bez tabletu kursor ich nie słucha.
- Autologon konta, na którym Agent testuje UI: zrzut i wejście działają na konsoli VM, więc bez zalogowanej sesji Agent widzi ekran logowania.
- Windows 11: OpenSSH Server z MSI `PowerShell/Win32-OpenSSH` (FoD `OpenSSH.Server` bywa `NotPresent`, ADR-0007), `sshd` automatycznie, reguła firewalla na 22. Dla konta z grupy Administratorzy klucz idzie do `C:\ProgramData\ssh\administrators_authorized_keys` z ACL tylko Administratorzy + SYSTEM, inaczej sshd go ignoruje. Windows OpenSSH zabija procesy sesji po jej zamknięciu: długie operacje synchronicznie albo przez zadanie harmonogramu.
- Kilka VM Windows z jednego obrazu: klony bez sysprepu (`sysprep /generalize /oobe /mode:vm`) mają ten sam SID i nie łączą się ze sobą po SMB/NTLM (Server 2025, Win11 24H2+, KB5070568; ADR-0005).
- Przed pracą Agenta zrób snapshot czystego stanu: `vm revert` wraca do nazwanego snapshotu.

## ssh

`afk` generuje jeden klucz ed25519 na Project (`~/.local/state/afk/<project>/vm-ssh/`) przy pierwszym użyciu albo przy `afk config`, który wypisuje część publiczną. Człowiek wkleja ją do `authorized_keys` konta `user` z `[[vm]]` na każdej VM. Klucz prywatny i konfiguracja ssh (alias na nazwę VM, bez zapamiętywania kluczy hosta, bo VM są cudze) są montowane do `~/.ssh` Sandboxa Chatu. Agent łączy się przez `ssh -o HostName=<adres z vm list> <vm> <polecenie>`.

## Własność

Pierwsza operacja zmieniająca (wszystkie poza `list` i `screenshot`) zajmuje VM dla Chatu (`flock` w `~/.local/state/afk/vm-locks/`) do `vm release` albo końca `afk chat`; inny Chat dostaje 409. `vm list` pokazuje właściciela.

## Adresy w `[expose]`

Do VM testowej publikuj porty na adres mostka libvirt (np. `192.168.122.1:8081:8080`), nie na `0.0.0.0` ([README](../README.md#expose)).
