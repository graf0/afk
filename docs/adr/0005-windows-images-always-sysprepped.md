# Obrazy Windows zawsze kończą się sysprepem

Każdy Base image Windows kończy budowę `sysprep /generalize /oobe /shutdown /mode:vm` z plikiem odpowiedzi dla klona (nazwa komputera z nazwy maszyny w `.afk.toml`, autologon dla sterowania ekranem). Duplikat SID maszyny przez lata był nieszkodliwy („The Machine SID Duplication Myth”, Russinovich 2009), ale od aktualizacji z 29.08.2025 (KB5064081/KB5065426) Windows 11 24H2/25H2 i Server 2025 odrzucają uwierzytelnianie NTLM/Kerberos między maszynami ze sklonowanym stanem (KB5070568, zdarzenie LSASRV 6167). Sprawdziliśmy to na własnych obrazach: klony bez sysprepu nie łączą się do siebie po SMB także z kontem lokalnym, w workgroupie, po IP i po zmianie nazwy; po sysprepie działają. Odrzuciliśmy drugi wariant obrazu bez sysprepu dla Run z jedną maszyną Windows: zysk ok. 40 s na start nie uzasadnia budowania i przebudowy dwóch wariantów.

## Consequences

Start klona Windows trwa ok. 55–60 s zamiast ok. 20 s; gotowość wykrywamy przez `IMAGE_STATE_COMPLETE` w `C:\Windows\Setup\State\State.ini`, bo sshd wstaje kilka sekund wcześniej.
Sysprep trzeba uruchamiać synchronicznie (Windows OpenSSH zabija procesy sesji po jej zamknięciu).
Plik odpowiedzi klona zawiera hasło autologonu w jawnej postaci — trzeba go usuwać po starcie.
Sysprep na Windows 11 nie był jeszcze testowany (ryzyko blokady przez pakiety Appx).
