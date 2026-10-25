# Project config i Base image zawsze z gałęzi bazowej

`.afk.toml` i `.claude/settings.json` są czytane z gałęzi bazowej (`origin/HEAD`) przez każde polecenie, które coś uruchamia: Factory, `afk image build`, `afk vm up`. Base image zbudowany z niezacommitowanej konfiguracji z drzewa roboczego trafiałby do Runów, choć gałąź bazowa go nie opisuje (luka w ADR-0002), a hash konfiguracji policzony z innego źródła niż w Factory wymuszałby od razu ponowną budowę. Jedyny wyjątek to `afk config`: waliduje plik z drzewa roboczego, żeby sprawdzić go przed commitem, i niczego nie uruchamia.

## Consequences

Zmiana `[machine.*]`, Tooli albo pluginów działa dopiero po merge'u do gałęzi bazowej; eksperyment z obrazem = branch bazowy w forku albo commit na gałęzi bazowej.
