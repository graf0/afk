# Host nie uruchamia niczego w klonie agenta

Agent kontroluje `.git/config` i hooki swojego klona, więc każdy git uruchomiony przez host w tym katalogu to kod agenta na hoście. Zamiast tego host robi wyłącznie `git fetch` (albo import `git bundle`) z VM do Quarantine repo i operuje na pobranych refach. Push, pull request i zmiany w Forge robi tylko `afk` z poświadczeniami hosta; w VM nie ma żadnych poświadczeń gita ani Forge. Przed pushem wynik jest walidowany: zmiany w chronionych ścieżkach (`.afk.toml`, `.github/`, `.gitea/`, hooki) powodują odrzucenie, a `.afk.toml` jest zawsze czytany z gałęzi bazowej hosta.

## Consequences

Hooki bezpieczeństwa Claude'a nie są elementem izolacji. Zastępuje `disarm_clone` z ralph.sh.
