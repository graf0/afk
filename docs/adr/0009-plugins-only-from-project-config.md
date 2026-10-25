# Pluginy Claude'a tylko z Project config, instalowane w Base image

Agent dostaje pluginy wyłącznie wymienione wprost: `enabledPlugins` i `extraKnownMarketplaces` z `.claude/settings.json` Projectu oraz `[agent].plugins` i `[agent].marketplaces` z `.afk.toml`. `afk image build` instaluje je w Base image (`claude plugin marketplace add` + `claude plugin install --scope user`); budowa ma pełny internet przez Egress proxy, Run dostaje tylko `[network].allow`. Z `~/.claude` hosta nic nie trafia do VM — ani w Factory, ani w Chat. Run jest przez to powtarzalny niezależnie od tego, co kto ma zainstalowane, a `afk` w ogóle nie czyta prywatnego katalogu użytkownika.

## Considered Options

- **Kopia katalogów pluginów z hosta przy starcie VM + `--plugin-dir`** (zrobione w #8, wycofane): biała lista, walidacja ścieżek, symlinków, hardlinków i bitów trybu — dużo kodu wokół granicy zaufania, a wynik Run zależał od stanu hosta.
- **`npx skills`** (vercel-labs/skills): instaluje gołe skille z dowolnego repo, ale wymaga Node w obrazie, łączy się z `registry.npmjs.org` i `skills.sh` (telemetria) i nie obsługuje pluginów projektu, które i tak idą przez `claude plugin install` — byłby drugim mechanizmem obok pierwszego.

## Consequences

Zmiana listy pluginów wymaga przebudowy Base image. Skille pluginu mają namespace (`/mattpocock-skills:implement`).
