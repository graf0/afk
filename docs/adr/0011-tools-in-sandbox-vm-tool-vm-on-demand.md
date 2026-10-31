---
status: superseded by ADR-0013
---

# Toole w Sandbox VM, Tool VM tylko na żądanie

Tool (Postgres, Redis, Playwright) domyślnie działa w Sandbox VM obok Agenta: instalowany w Base image (`build`), uruchomiony od startu VM, przygotowany przez `setup` po klonie. Każdy Run ma przez to własną, jednorazową instancję — Agenty sobie nie przeszkadzają, Agent ma bezpośredni dostęp (unix socket, `pg_dump`), bez haseł (to nie sekrety: instancja żyje w izolowanej VM), bez dodatkowego RAM na maszynę. Osobna Tool VM (`on = "<maszyna>"`) jest tylko dla tego, czego Sandbox VM nie pomieści (Windows, inny system) i podnosi się na żądanie Agenta przez kanał gość → `afk`, wspólny z `afk-cli`. Agent poznaje każdy Tool tak samo (`prompt`, `env`), bez względu na to, gdzie działa.

## Considered Options

- **Tool `host-port`** (usługa hosta, np. Postgres, przez `ssh -R`): pierwsza droga z Sandbox VM do hosta poza Egress proxy (ADR-0001), a usługa hosta zwykle trzyma dane spoza Projectu i jest współdzielona przez równoległe Runy.
- **Każdy Tool jako Tool VM z „capabilities”**: jednolite, ale RAM i czas startu na każdy Run, sieć z wieloma VM i utrata bezpośredniego dostępu do usługi.
- **Tool `package`** obok `[image].provision`: dwie drogi do tego samego; zastąpione jednym kluczem `build` w maszynie i w Toolu.
