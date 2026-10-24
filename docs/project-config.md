# `.afk.toml`

Szkielet (nazwy sekcji ustalone, klucze wstępne). Plik czytany zawsze z gałęzi bazowej; agent nie może go zmienić (ADR-0002).

```toml
[forge]
kind = "github"            # github | gitea
repo = "graf0/dfmx"
# url = "https://gitea.example"   # dla gitea

[labels]                    # nadpisuje domyślny słownik
ready = "ready-for-agent"
needs_info = "needs-info"

[agent]
model = "..."
review_model = "..."        # inny niż model
factory_plugins = ["mattpocock-skills@claude-plugins-official"]  # biała lista pluginów użytkownika w Factory, domyślnie pusta; Chat dostaje wszystkie

[gate]
cmd = "task lint && go test ./..."
max_fixes = 3

[image]
template = "debian13"       # debian13 | ws2025 | win11
max_age = "720h"            # Expiry: po upływie przebudowa (dla Windows z ISO)
provision = ["..."]         # faza budowy: internet przez proxy, bez sekretów

[network]
allow = ["proxy.golang.org", "sum.golang.org"]   # allowlista Run, domyślnie pusta

[protect]
paths = [".afk.toml", ".github/", ".gitea/", ".claude/"]

[tool.postgres]
kind = "package"            # host-port | package | vm
```

Windows: ścieżka i SHA256 ISO podaje użytkownik w konfiguracji; odpowiedzialność licencyjna po jego stronie. Bez obejść `rearm`.
