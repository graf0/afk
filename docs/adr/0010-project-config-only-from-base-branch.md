---
status: accepted (zakres zmieniony przez ADR-0013, patrz Update)
---

# Project config i Base image zawsze z gałęzi bazowej

`.afk.toml` i `.claude/settings.json` są czytane z gałęzi bazowej (`origin/HEAD`) przez każde polecenie, które coś uruchamia: Factory, `afk image build`, `afk vm up`. Base image zbudowany z niezacommitowanej konfiguracji z drzewa roboczego trafiałby do Runów, choć gałąź bazowa go nie opisuje (luka w ADR-0002), a hash konfiguracji policzony z innego źródła niż w Factory wymuszałby od razu ponowną budowę. Jedyny wyjątek to `afk config`: waliduje plik z drzewa roboczego, żeby sprawdzić go przed commitem, i niczego nie uruchamia.

## Consequences

Zmiana `[machine.*]`, Tooli albo pluginów działa dopiero po merge'u do gałęzi bazowej; eksperyment z obrazem = branch bazowy w forku albo commit na gałęzi bazowej.

## Update (ADR-0013)

Reguła obejmuje dziś `.afk.toml`, `.claude/settings.json`, `compose.yml`, `compose.afk.yml` i Dockerfile'e Stacku, czytane przez `afk chat` i `afk factory`. Komend `afk image build` i `afk vm up` już nie ma; obrazy Stacku budują się z gałęzi bazowej same.
