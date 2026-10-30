---
status: accepted
---

# `afk` działa tylko na pierwszym planie, bez TUI

Zastępuje ADR-0008.

`afk chat` i `afk factory` trzymają terminal, dopóki działają, a Stack żyje tylko razem z procesem `afk` (`compose down` przy wyjściu). Stan Chat i Run (klon, home Agenta, volume'y) zostaje w katalogu stanu: `afk chat <nazwa>` do niego wraca, `afk factory` wznawia porzucony Run, a usuwa go dopiero `afk chat rm` albo `--rm`. Katalog stanu jest jedyną ewidencją; `afk` nie trzyma osobnej. Kilka Chatów naraz to kilka terminali albo `screen`/`tmux` uruchomione przez człowieka. TUI na tmux istniało, żeby Egress proxy i połączenie ssh przeżyły zamknięcie terminala. Po przejściu na kontener (ADR-0013) ten powód znika, a zostaje koszt utrzymania sidebaru, akcji i osobnego serwera tmux.
