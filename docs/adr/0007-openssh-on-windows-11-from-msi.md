# OpenSSH na Windows 11 instalujemy z MSI, nie jako Feature on Demand

Windows Server 2025 ma OpenSSH Server wbudowany, ale na Windows 11 (obraz z ISO) funkcja `OpenSSH.Server` ma stan `NotPresent` i jej instalacja wymaga Windows Update: `Add-WindowsCapability` przez ssh kończy się odmową dostępu (WUA), a jako SYSTEM błędem `0x8024402c`, bo Windows Update ignoruje proxy WinHTTP, a w budowie nie ma innej drogi do internetu niż Egress proxy. Dlatego Template Windows 11 instaluje OpenSSH z MSI `PowerShell/Win32-OpenSSH` (`OpenSSH-Win64-v10.4.0.0.msi`, tag `10.4.0.0p1-Preview`, SHA256 `788d8643464a01eb63640a2fd746f9ed23d106b5a901a35f88dc8cd40e446ac9`) podanego na płycie instalacyjnej, z przypiętą sumą. Odrzuciliśmy FoD z ISO „Languages and Optional Features” (kolejne ISO do pobierania przez użytkownika) i otwieranie Windows Update przez ruch bez proxy (łamie ADR-0001).

## Consequences

`afk-setup.ps1` wybiera ścieżkę po stanie: jeśli `OpenSSH.Server` jest `Installed` (Server), używa wbudowanego, w przeciwnym razie instaluje MSI.
MSI trzeba uruchamiać w fazie `firstlogon`; w `specialize` kończy się kodem 1603.
Wersja MSI to wydanie oznaczone jako Preview; podbicie wymaga zmiany sumy i przebudowy obrazów Win11.
