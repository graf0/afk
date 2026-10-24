# Task format

Ticket text is Polish. Every task is one checkbox:

```
- [ ] **<short name>** (~<time>)
  1. <concrete action with concrete data>
  2. ...
  Oczekiwane: <observable result>
  Dlaczego: <file, issue or PR comment>
```

- **Time**: honest estimate, `~2 min` or `~10 min`.
- **Steps** name real data from the dev seed or fixtures (configs, systems, templates), not "some config". Only UI, agent or sftp/ssh actions.
- **Dlaczego** points at a source the tester can open. A task born from an agent note like "nie sprawdzałem w przeglądarce" goes in **Podejrzane miejsca**.

## Sections

- **Przygotowanie**: commands from `Taskfile.yml` (e.g. `task dev`, `task agents`), seed data, prerequisites the repo can't provide.
- **Podejrzane miejsca**: tasks derived from agent doubts and unverified claims. Highest bug probability, so first.
- **#N <closed issue title>**: one section per closed issue, tasks ordered riskiest first.
- **Regresja**: a few quick checks of neighbouring features the diff could have broken.

## Example

```
- [ ] **`/.error` w listingu korzenia sftp** (~3 min)
  1. W UI utwórz config w systemie z seeda, dodaj rolę z receptą `cookbook/vpn.yml`.
  2. Przez sftp (`sftp` i `dfmx -u`) spróbuj skasować `cookbook/vpn.yml`.
  3. Wylistuj korzeń, potem odczytaj `/.error`.
  Oczekiwane: permission denied, `/.error` wymienia config, system i rolę; plik nie zaśmieca listingu korzenia.
  Dlaczego: #40, komentarz „Wątpliwości” o `/.error` widocznym w listingu korzenia.
```
