# Base image budujemy Packerem CLI, którym zarządza afk

Zastępuje ADR-0004. Obrazy buduje binarka `packer` uruchamiana przez `afk` jako podproces, z szablonami HCL wbudowanymi w `afk` (`go:embed`) i wartościami z `.afk.toml` przekazanymi przez wygenerowany `-var-file`; postęp i błędy czytamy z `-machine-readable`, a przerwanie `afk` wysyła Packerowi SIGINT (sam sprząta QEMU). Wersja Packera i pluginu `qemu` jest przypięta dokładnie: `afk` pobiera Packera z releases.hashicorp.com (binarek nie ma w release'ach na GitHubie) do własnego katalogu, weryfikuje SHA256 zapisany w kodzie, ignoruje `packer` z `PATH`, a plugin instaluje `packer init` do `PACKER_PLUGIN_PATH` ustawionego na katalog `afk`. Provisioning robią natywne provisionery Packera (`shell`, `powershell`, `windows-restart`), a internet daje `ssh_remote_tunnels` do Egress proxy `afk`. Biblioteka (ADR-0004) działała, ale wymagała `replace` dla `go-cty`, ok. 150 zależności i opierała się na SDK, które nie jest przeznaczone do osadzania; CLI ma udokumentowany, stabilny interfejs (schemat HCL), a klucze buildera są te same.

## Consequences

Packer ma licencję BUSL 1.1; `afk` (MIT) jej nie dystrybuuje, tylko pobiera w czasie działania — użycie wewnętrzne jest dozwolone.
Na czas budowy Egress proxy słucha na losowym porcie loopbacku hosta (lokalne procesy mogą z niego skorzystać, z tą samą polityką „bez adresów prywatnych i hosta”); w Run nadal nie ma żadnego portu po stronie hosta (ADR-0001).
`afk` weryfikuje tylko SHA256 przypięty w kodzie; podpis GPG HashiCorp (`C874 011F 0AB4 0511 0D02 1055 3436 5D94 72D7 468F`) sprawdza człowiek przy podbijaniu wersji — Go nie ma OpenPGP w stdlib, a `x/crypto/openpgp` jest przestarzały.
Sieć budowy pozostaje `user,restrict=on` z hostfwd tylko dla ssh (przez `qemuargs`), `cpu_model=host`, obraz UEFI to dysk plus `efivars.fd`.
