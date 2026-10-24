#!/usr/bin/env bash
# Ralph loop: dopóki są niezablokowane issue `ready-for-agent`, bierze najstarsze,
# robi je świeżymi sesjami claude w klonie <repo>/.ralph/ticket-N i wystawia PR.
# Zwykły ticket = jedna sesja; spec (issue z sub-issues) = sesja na każde dziecko + code review.
# Model bezpieczeństwa: każda sesja claude, bramka i każdy git w klonie agenta działa WYŁĄCZNIE przez `sandbox`
# (bubblewrap, bez sekretów: SSH, token gh). Host nigdy nie uruchamia gita ani niczego innego w katalogu klonu,
# gdy agent już go dotknął — agent kontroluje `.git/config` i hooki klonu, więc to byłby kod agenta na hoście.
# Host robi tylko `git fetch <klon> HEAD` z głównego repo i operuje na FETCH_HEAD w głównym repo.
# Push, PR i zmiany na issue robi wyłącznie ten skrypt.
# Opis przepływu: README.md, sekcja „Ralph (agent AFK)”.
# Użycie: ./ralph.sh [max_iteracji=10]
set -Eeuo pipefail
shopt -s inherit_errexit
# Nieobsłużony błąd (set -e) to też twarda porażka; w podpowłokach milczymy, rodzic zgłosi.
trap 'rc=$?; if [[ $BASHPID == "$$" ]]; then fail "nieoczekiwany błąd (kod $rc, linia $LINENO): $BASH_COMMAND"; fi' ERR

impl_model=claude-sonnet-5-5 impl_effort=medium
review_model=claude-opus-5-5 review_effort=high
# Ile sesji naprawczych dostaje agent, gdy bramka go build/vet/test padnie.
max_gate_fixes=3
gh_min_version=2.60.0
gate_cmd='go build ./... && go vet ./... && go test ./...'
# resume=1: pierwsza sesja bierze istniejący klon zamiast świeżego; resume_note trafia do jej promptu.
resume= resume_note=
# Pluginy wyłączane w sesjach agenta (przez settings.local.json w klonie).
disabled_plugins=(
	ponytail@ponytail
	superpowers@claude-plugins-official
	frontend-design@claude-plugins-official
	ruby-lsp@claude-plugins-official
)

# Wyjście: "= " szare — kod skryptu, "= " zielone — start ticketa/speca/wznowienia/review,"! " czerwone — twarda porażka, bez prefiksu — agent.
if [[ -t 2 ]]; then
	gray=$'\e[37m' red=$'\e[31m' green=$'\e[32m' off=$'\e[0m'
else
	gray= red= green= off=
fi

# say MSG...: linia skryptu.
say() { printf '%s= %s%s\n' "$gray" "$*" "$off" >&2; }

# announce MSG...: jak say, ale zielone — start ticketa/speca/wznowienia/review.
announce() { printf '%s= %s%s\n' "$green" "$*" "$off" >&2; }

# quote CMD...: uruchamia CMD skryptu, jego wyjście idzie jako linie "= ".
quote() {
	"$@" 2>&1 | while IFS= read -r line; do say "$line"; done
}

# Raport agenta: summary/deviations/doubts trafiają do komentarza na PR, findings_* wypełnia tylko review.
schema='{"type":"object","properties":{"status":{"type":"string","enum":["done","needs_info"]},"reason":{"type":"string"},"summary":{"type":"string"},"deviations":{"type":"array","items":{"type":"string"}},"doubts":{"type":"array","items":{"type":"string"}},"findings_fixed":{"type":"array","items":{"type":"string"}},"findings_skipped":{"type":"array","items":{"type":"string"}}},"required":["status","reason","summary","deviations"]}'

# disarm_clone: po twardej porażce odbiera klonowi kod agenta (hooki, config), żeby dało się go bezpiecznie
# oglądać w edytorze. Tylko operacje na plikach, bez gita w klonie. Najpierw obrona przed symlinkami:
# agent mógł podmienić $run/repo albo .git na link do hosta, a rm w jego wnętrzu niszczyłby pliki hosta.
# $run jest kanoniczne (root z `pwd -P`).
disarm_clone() {
	local g=$run/repo/.git url
	[[ -e $run/repo || -L $run/repo ]] || return 0
	if [[ ! -L $run/repo && ! -L $g && -d $g && $(realpath -e "$g") == "$g" ]]; then
		url=$(git -C "$root" remote get-url origin) || url=
		# commondir przekierowuje git na cudzy config; zagnieżdżone .git to osobne repo z configiem agenta.
		rm -rf "$g/hooks" "$g/config" "$g/config.worktree" "$g/commondir"
		find "$run/repo" -mindepth 2 -name .git -prune -exec rm -rf {} +
		printf '[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n[remote "origin"]\n\turl = %s\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n' "$url" >"$g/config"
		mkdir "$g/hooks"
	else
		if [[ -L $run/repo ]]; then
			rm -f "$run/repo"
		else
			rm -rf "$g"
		fi
		printf '%s! klon jest bez historii gita (podejrzane symlinki w %s)%s\n' "$red" "$run/repo" "$off" >&2
	fi
}

fail() {
	printf '%s! %s%s\n' "$red" "$*" "$off" >&2
	if [[ -n ${run:-} ]]; then
		disarm_clone || true
	fi
	exit 1
}

# sandbox RUN CMD...: uruchamia CMD w bubblewrap; zapis tylko w RUN i w cache. Wymaga $token, $cache_dir, $claude_bin.
sandbox() {
	local run=$1 resolv
	shift
	local args=(
		--unshare-all --share-net --die-with-parent --new-session --clearenv
		--proc /proc --dev /dev --tmpfs /tmp --tmpfs "$HOME"
		--ro-bind /usr /usr --ro-bind /etc /etc --ro-bind /opt /opt --ro-bind /usr/local/go /usr/local/go
		--symlink usr/bin /bin --symlink usr/sbin /sbin --symlink usr/lib /lib --symlink usr/lib64 /lib64
	)
	# /etc/resolv.conf bywa symlinkiem do /run/..., bez celu DNS nie działa.
	resolv=$(readlink -f /etc/resolv.conf)
	if [[ $resolv != /etc/* ]]; then
		args+=(--ro-bind "$resolv" "$resolv")
	fi
	args+=(
		--ro-bind "$HOME/.local/share/claude" "$HOME/.local/share/claude"
		--ro-bind "$HOME/.claude/plugins" "$HOME/.claude/plugins"
		--ro-bind "$HOME/.claude/settings.json" "$HOME/.claude/settings.json"
		--ro-bind "$HOME/go/bin/golangci-lint" "$HOME/go/bin/golangci-lint"
		--ro-bind "$HOME/go/bin/gopls" "$HOME/go/bin/gopls"
		--bind "$run" "$run" --bind "$cache_dir" "$cache_dir"
		--chdir "$run/repo"
		--setenv HOME "$HOME"
		--setenv PATH "$HOME/go/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
		--setenv TERM "${TERM:-xterm}"
		--setenv LANG "${LANG:-C.UTF-8}"
		--setenv CLAUDE_CODE_OAUTH_TOKEN "$token"
		--setenv GOMODCACHE "$cache_dir/gomod"
		--setenv GOCACHE "$cache_dir/gobuild"
		--setenv npm_config_cache "$cache_dir/npm"
		--setenv DISABLE_AUTOUPDATER 1
	)
	bwrap "${args[@]}" "$@"
}

issue() { gh api "repos/{owner}/{repo}/issues/$1"; }

# blocked ISSUE_JSON: natywne dependencies albo otwarte issue z linii `Blocked by: #a, #b` w body.
blocked() {
	local n
	# Liczymy po state blokerów, nie po issue_dependencies_summary.blocked_by: ten licznik spóźnia się po `gh issue close`.
	if (($(gh api "repos/{owner}/{repo}/issues/$(jq .number <<<"$1")/dependencies/blocked_by" --jq '[.[] | select(.state == "open")] | length') > 0)); then
		return 0
	fi
	for n in $(jq -r '.body // ""' <<<"$1" | grep -i '^blocked by:' | grep -o '#[0-9]\+' | tr -d '#'); do
		if [[ $(gh issue view "$n" --json state --jq .state) == OPEN ]]; then
			return 0
		fi
	done
	return 1
}

# open_children N: otwarte sub-issues, rosnąco po numerze (= po dacie utworzenia).
open_children() {
	gh api "repos/{owner}/{repo}/issues/$1/sub_issues?per_page=100" \
		--jq '[.[] | select(.state == "open")] | sort_by(.number)'
}

# pick: "N single" albo "N spec" — najniższy numer `ready-for-agent` bez otwartego blokera; pusto, gdy brak.
# Dzieci speców pomija (robi je spec). Spec bez dzieci albo z dzieckiem bez `ready-for-agent` pomija z ostrzeżeniem.
pick() {
	local n json
	for n in $(gh issue list --state open --label ready-for-agent --limit 100 --json number --jq 'sort_by(.number) | .[].number'); do
		# Lista po labelu bywa nieaktualna przez kilka sekund po jego zdjęciu — sprawdzamy świeży JSON
		# i pomijamy tickety obsłużone już w tym uruchomieniu.
		[[ " ${handled[*]} " != *" $n "* ]] || continue
		json=$(issue "$n")
		jq -e 'any(.labels[]; .name == "ready-for-agent")' <<<"$json" >/dev/null || continue
		if [[ $(jq '.parent_issue_url' <<<"$json") != null ]] || blocked "$json"; then
			continue
		fi
		if (($(jq '.sub_issues_summary.total // 0' <<<"$json") > 0)); then
			if [[ -n $(open_children "$n" | jq '.[] | select([.labels[].name] | index("ready-for-agent") | not)') ]]; then
				say "pomijam spec #$n — ma otwarte dziecko bez ready-for-agent"
				continue
			fi
			echo "$n spec"
			return
		fi
		if jq -r '.body // ""' <<<"$json" | grep -q '^## User Stories'; then
			say "pomijam spec #$n — nie ma ticketów, odpal /to-tickets #$n"
			continue
		fi
		echo "$n single"
		return
	done
}


# fresh_clone START: świeży klon origin w $run/repo na branchu START (main albo ticket-<spec>); ustawia base.
# Klon jest tu jeszcze zaufany; w sandboxie nie ma ~/.gitconfig.
fresh_clone() {
	rm -rf "$run"
	mkdir -p "$run"
	quote git clone -q --reference-if-able "$root" --dissociate --branch "$1" "$(git remote get-url origin)" "$run/repo"
	if [[ $1 != "$branch" ]]; then
		git -C "$run/repo" checkout -q -b "$branch"
	fi
	git -C "$run/repo" config user.name "$(git config user.name)"
	git -C "$run/repo" config user.email "$(git config user.email)"
	# base z klonu, nie z origin/main hosta — main mógł się ruszyć między fetchem a klonem.
	base=$(git -C "$run/repo" rev-parse HEAD)
	cp "$root/.env" "$run/repo/.env"
}

# resume_clone: istniejący klon po twardej porażce. Klon mógł zostać bez rozbrojenia (Ctrl-C), więc najpierw disarm
# i usunięcie symlinków, które agent mógł podłożyć pod pliki zapisywane przez hosta. Git w klonie tylko przez sandbox.
# Ustawia base: koniec brancha z remote, a gdy go nie ma — punkt odejścia klona od origin/main.
resume_clone() {
	local p
	disarm_clone
	[[ -d $run/repo/.git ]] || fail "klon bez historii gita, nie da się wznowić (katalog: $run)"
	# land() pushuje squash, którego klon nie zawiera: po przerwaniu między landem a następnym klonem
	# klon rozjeżdża się z remote, a wznowienie skończyłoby się „agent przepisał historię”. Wtedy świeży klon.
	if git rev-parse -q --verify "refs/remotes/origin/$branch" >/dev/null; then
		git fetch -q --no-recurse-submodules "$run/repo" HEAD || fail "nie udało się pobrać HEAD klona (katalog: $run)"
		if ! git merge-base --is-ancestor "refs/remotes/origin/$branch" FETCH_HEAD; then
			say "klon rozjechał się z origin/$branch (squash z landa) — biorę świeży klon, niezapushowana praca ze starego przepada"
			fresh_clone "$branch"
			return
		fi
	fi
	for p in .claude .git/info .git/info/exclude; do
		if [[ -L $run/repo/$p ]]; then rm -f "$run/repo/${p:?}"; fi
	done
	rm -f "$run/repo/.env"
	cp "$root/.env" "$run/repo/.env"
	sandbox "$run" git config user.name "$(git config user.name)"
	sandbox "$run" git config user.email "$(git config user.email)"
	if git rev-parse -q --verify "refs/remotes/origin/$branch" >/dev/null; then
		base=$(git rev-parse "refs/remotes/origin/$branch")
	else
		git fetch -q --no-recurse-submodules "$run/repo" HEAD || fail "nie udało się pobrać HEAD klona (katalog: $run)"
		base=$(git merge-base FETCH_HEAD origin/main) || fail "klon nie ma wspólnej historii z origin/main (katalog: $run)"
	fi
	human=1
	resume_note=$'\n\n'"Wznawiasz przerwaną pracę: klon zawiera Twój wcześniejszy stan (commity, być może niezacommitowane zmiany). Sprawdź \`git log origin/main..HEAD\` i \`git status\`, ustal na jakim etapie skończyłeś i dokończ tylko to, co zostało; nie zaczynaj od nowa."
}

# get_clone START MODEL EFFORT: klon do pierwszej sesji — istniejący przy wznowieniu, inaczej świeży; plus ustawienia sesji.
get_clone() {
	if [[ -n $resume ]]; then
		resume_clone
		resume=
	else
		fresh_clone "$1"
	fi
	session_settings "$2" "$3"
}

# session_settings MODEL EFFORT: model, effort i wyłączone pluginy w $run/repo/.claude/settings.local.json.
# Wołać po fresh_clone i przed run_claude, póki klon jest zaufany. Plik trafia do .git/info/exclude:
# w sandboxie nie ma globalnego gitignore, więc agent by go zacommitował, a filtr `.claude/` odrzuciłby ticket.
session_settings() {
	mkdir -p "$run/repo/.claude" "$run/repo/.git/info"
	rm -f "$run/repo/.claude/settings.local.json"
	jq -n --arg model "$1" --arg effort "$2" \
		'{model: $model, effortLevel: $effort, enabledPlugins: ($ARGS.positional | map({(.): false}) | add)}' \
		--args "${disabled_plugins[@]}" >"$run/repo/.claude/settings.local.json"
	grep -qxF ".claude/settings.local.json" "$run/repo/.git/info/exclude" 2>/dev/null ||
		echo ".claude/settings.local.json" >>"$run/repo/.git/info/exclude"
}

# agent_lines: ze strumienia stream-json wypisuje tekst agenta i jednolinijkowe streszczenie wywołań narzędzi (szare: $c…$o).
agent_lines='fromjson? | select(.type == "assistant") | .message.content[]
	| if .type == "text" then .text
	elif .type == "tool_use" and .name != "StructuredOutput" then "\($c)→ \(.name) \(.input.command // .input.file_path // .input.pattern // .input.description // "" | tostring | split("\n")[0])\($o)"
	else empty end'

# run_claude LOG PROMPT: sesja claude w sandboxie; ustawia status, reason i raport (summary, deviations, doubts,
# findings_fixed, findings_skipped jako listy markdown), inaczej twarda porażka. Ustawia human=1, gdy człowiek ma coś sprawdzić.
run_claude() {
	local result prompt=$2$resume_note
	resume_note=
	if ! sandbox "$run" "$claude_bin" -p --dangerously-skip-permissions --verbose --output-format stream-json \
		--json-schema "$schema" "$prompt" | tee "$1" | jq --unbuffered -rR --arg c "$gray" --arg o "$off" "$agent_lines"; then
		fail "claude zakończył się błędem (log: $1, katalog: $run)"
	fi
	result=$(jq -cR 'fromjson? | select(.type == "result")' "$1" | tail -n 1)
	status=$(jq -r '.structured_output.status // empty' <<<"$result") || fail "niepoprawny JSON (log: $1)"
	reason=$(jq -r '.structured_output.reason // empty' <<<"$result")
	summary=$(jq -r '.structured_output.summary // empty' <<<"$result")
	deviations=$(jq -r '(.structured_output.deviations // [])[] | "- " + .' <<<"$result")
	doubts=$(jq -r '(.structured_output.doubts // [])[] | "- " + .' <<<"$result")
	findings_fixed=$(jq -r '(.structured_output.findings_fixed // [])[] | "- " + .' <<<"$result")
	findings_skipped=$(jq -r '(.structured_output.findings_skipped // [])[] | "- " + .' <<<"$result")
	if [[ -n $deviations$doubts$findings_skipped ]]; then
		human=1
	fi
}

# report TITLE: raport ostatniej sesji jako markdown (komentarz na PR).
report() {
	printf '### %s\n\n%s\n' "$1" "$summary"
	if [[ -n $deviations ]]; then printf '\n**Odstępstwa od speca:**\n%s\n' "$deviations"; fi
	if [[ -n $doubts ]]; then printf '\n**Wątpliwości do sprawdzenia:**\n%s\n' "$doubts"; fi
	if [[ -n $findings_fixed ]]; then printf '\n**Poprawione w review:**\n%s\n' "$findings_fixed"; fi
	if [[ -n $findings_skipped ]]; then printf '\n**Pominięte w review:**\n%s\n' "$findings_skipped"; fi
}

# pr_number: numer otwartego PR brancha $branch (pusto, gdy brak).
pr_number() {
	gh pr list --head "$branch" --state open --json number --jq '.[0].number // empty'
}

# pr_comment BODY: komentarz na PR $pr; limit komentarza GH to 65536 znaków.
pr_comment() {
	quote gh pr comment "$pr" --body "$(head -c 60000 <<<"$1")"
}

# pr_labels: ustawia labels na (--label ready-for-human), gdy człowiek ma coś sprawdzić.
pr_labels() {
	labels=()
	if [[ -n $human ]]; then
		labels=(--label ready-for-human)
	fi
}

# rules: wspólne zasady dla sesji implementującej.
rules() {
	cat <<EOF2
Zasady:
- Pracujesz na gotowym branchu \`$branch\` w świeżym klonie repo (bieżący katalog). Nie zmieniaj brancha.
- Commituj swoją pracę. Nie pushuj, nie twórz PR, nie wołaj \`gh\` (nie masz poświadczeń) — to zrobi skrypt.
- Taska uruchamiaj jako \`go tool task\` (nie ma go w PATH).
- Jeśli zmieniasz frontend (\`server/web\`), najpierw \`npm ci\` w \`server/web\`.
- Testy z bazą używają Postgresa pod \`localhost:5432\` z \`DATABASE_TEST_URL\` z \`.env\` (ładuje go \`dbtest\`; przez socket unixowy się nie połączysz). Nie uruchamiaj własnego Postgresa (\`initdb\`, \`pg_ctl\`). Jeśli baza jest niedostępna, zwróć \`needs_info\`.
- Na koniec zostaw czyste drzewo robocze (wszystko zacommitowane).
$(context_note)
- Zwróć status \`done\`, gdy ticket jest zrobiony i zacommitowany: w \`summary\` 2-5 zdań co zrobiono, w \`deviations\` każde odstępstwo od ticketu/speca z powodem (pusta lista, gdy nie było), w \`doubts\` rzeczy do sprawdzenia przez człowieka (opcjonalnie), w \`reason\` jedno zdanie. Zwykłe odstępstwo z dobrego powodu tylko zgłoś, nie zatrzymuj się.
- Zwróć \`needs_info\` z powodem w \`reason\` (\`summary\` puste, \`deviations\` pusta lista), gdy ticket jest niejasny, sprzeczny albo za duży — wtedy bez commitów.
EOF2
}

ticket_json() {
	gh issue view "$1" --json number,title,body,comments --jq '{number, title, body, comments: [.comments[].body]}'
}

# issue_md N: tytuł, body i komentarze issue jako markdown.
issue_md() {
	gh issue view "$1" --json number,title,body,comments \
		--jq '"# #\(.number) \(.title)\n\n\(.body)" + ([.comments[].body | "\n\n---\nKomentarz:\n\(.)"] | join(""))'
}

# write_context TICKET [SPEC]: treść ticketu i speca do $run/ticket.md i $run/spec.md (poza repo, bez commita).
# Sesje claude nie mają `gh`, a subagenci (np. code-review) nie dziedziczą promptu, więc dostają ścieżki.
# Wołać po fresh_clone/get_clone (ten kasuje $run) i przed run_claude.
write_context() {
	rm -f "$run/ticket.md" "$run/spec.md"
	if [[ -n $1 ]]; then issue_md "$1" >"$run/ticket.md"; fi
	if [[ -n ${2:-} ]]; then issue_md "$2" >"$run/spec.md"; fi
}

# context_files: istniejące pliki z write_context, po spacji.
context_files() {
	local f
	for f in "$run/ticket.md" "$run/spec.md"; do
		if [[ -f $f ]]; then printf '%s ' "$f"; fi
	done
}

# context_note: zasady o źródle treści ticketu/speca, zależnie od tego, które pliki z write_context istnieją.
context_note() {
	local files
	files=$(context_files)
	[[ -n $files ]] || return 0
	cat <<EOF2
- Treść zgłoszeń leży w plikach: ${files}(ticket w \`ticket.md\`, spec w \`spec.md\`, o ile istnieją), poza repo, nie commituj ich. To jedyne źródło: nie masz dostępu do trackera ani \`gh\`, więc nie szukaj zgłoszeń w sieci ani w historii commitów i nie zgaduj brakujących szczegółów. Gdy zgłoszenie odsyła do czegoś, czego tam nie ma, zwróć \`needs_info\`. Podaj te ścieżki subagentom (np. code-review): nie dziedziczą tego promptu.
- Nikt nie odpowie na pytania. Seamy testowe są uzgodnione w sekcji „Testing Decisions” speca; gdy speca nie ma, użyj seamów najbliższych istniejących testów w repo i wymień wybór w \`summary\`.
EOF2
}

check_clean() {
	[[ -z $(sandbox "$run" git status --porcelain) ]] || fail "agent zostawił niezacommitowane zmiany (katalog: $run)"
}

# join_lines A B: A i B w osobnych liniach, bez pustej linii, gdy któreś jest puste.
join_lines() {
	if [[ -n $1 && -n $2 ]]; then
		printf '%s\n%s' "$1" "$2"
	else
		printf '%s%s' "$1" "$2"
	fi
}

# gate BASE: go build/vet/test w sandboxie. Gdy padnie, agent dostaje output i poprawia (do $max_gate_fixes sesji);
# twarda porażka dopiero po wyczerpaniu prób albo gdy agent nie zwróci done (np. needs_info).
# Output każdej próby: $R/_logs/$branch-gate-N.txt. Raport sesji naprawczej dokleja się do raportu agenta.
gate() {
	local try out log line prev_summary prev_deviations prev_doubts prev_fixed prev_skipped
	for ((try = 1; ; try++)); do
		log=$R/_logs/$branch-gate-$try.txt
		if out=$(sandbox "$run" bash -c "$gate_cmd" 2>&1); then
			say "bramka przeszła"
			return
		fi
		printf '%s\n' "$out" >"$log"
		say "bramka nie przeszła (próba $try, pełny output: $log); koniec outputu:"
		tail -n 40 "$log" | while IFS= read -r line; do say "$line"; done
		((try <= max_gate_fixes)) || fail "bramka go build/vet/test nie przeszła po $max_gate_fixes sesjach naprawczych (log: $log, katalog: $run)"

		say "$branch: sesja naprawcza bramki $try/$max_gate_fixes"
		prev_summary=$summary prev_deviations=$deviations prev_doubts=$doubts
		prev_fixed=$findings_fixed prev_skipped=$findings_skipped
		run_claude "$R/_logs/$branch-gate-$try.json" "Bramka \`go build ./... && go vet ./... && go test ./...\` nie przeszła na Twojej pracy w tym klonie. Koniec jej outputu:

$(tail -n 200 "$log")

Napraw przyczynę minimalnie, nie zmieniaj zakresu ticketu, uruchom bramkę i zacommituj poprawkę. Zwróć \`needs_info\` tylko wtedy, gdy naprawa jest niemożliwa bez decyzji człowieka.

$(rules)"
		[[ $status == done ]] || fail "agent nie naprawił bramki (status: ${status:-brak}): $reason (log: $log, katalog: $run)"
		summary=$prev_summary$'\n\n'"Poprawka po bramce ($try): $summary"
		deviations=$(join_lines "$prev_deviations" "$deviations")
		doubts=$(join_lines "$prev_doubts" "$doubts")
		findings_fixed=$prev_fixed findings_skipped=$prev_skipped
		human=1
		check_clean
		fetch_head "$1"
	done
}

# fetch_head BASE: pobiera HEAD klona do głównego repo; ustawia head. Historia musi zawierać BASE.
fetch_head() {
	git fetch -q --no-recurse-submodules "$run/repo" HEAD || fail "nie udało się pobrać HEAD klona (katalog: $run)"
	head=$(git rev-parse FETCH_HEAD)
	git merge-base --is-ancestor "$1" "$head" || fail "agent przepisał historię — BASE nie jest przodkiem HEAD (katalog: $run)"
}

# land BASE MSG: czysto → fetch → bramka → squash do jednego commita na BASE (body: summary agenta) → push; ustawia base na nowy commit.
land() {
	local new
	check_clean
	fetch_head "$1"
	[[ -n $(git rev-list "$1..$head") ]] || fail "status done, ale brak commitów (katalog: $run)"
	gate "$1"
	new=$(git commit-tree "$head^{tree}" -p "$1" -m "$2"$'\n\n'"$summary")
	if git diff --no-renames --name-only "$1" "$new" | grep -qE '^(\.github/|\.claude/|ralph\.sh$)'; then
		fail "diff dotyka .github/, .claude/ lub ralph.sh (katalog: $run)"
	fi
	quote git push -q origin "$new:refs/heads/$branch"
	base=$new
}

cleanup() {
	rm -rf "$run"
}

# review N TITLE: sesja code review całego brancha względem origin/main (świeży klon z $branch);
# poprawki, jeśli są, lądują jako jeden commit "code review: TITLE"; ustawia review_report.
review() {
	announce "#$1: code review"
	get_clone "$branch" "$review_model" "$review_effort"
	if [[ $3 == spec ]]; then write_context "" "$1"; else write_context "$1"; fi
	run_claude "$R/_logs/ticket-$1-review.json" "/mattpocock-skills:code-review origin/main

Źródło speca dla osi Spec: $(context_files). Zakres zmian: \`origin/main..HEAD\`. Nie szukaj zgłoszeń w trackerze ani przez \`gh\` (nie masz poświadczeń) i nie pytaj użytkownika, gdzie jest spec; przekaż te ścieżki subagentom.

Po review popraw wszystkie znaleziska i zacommituj poprawki. Nie pushuj, nie wołaj \`gh\`, nie zmieniaj brancha \`$branch\`. Jeśli nie ma czego poprawiać — bez commita. Zwróć status \`done\`: w \`summary\` krótki werdykt, w \`findings_fixed\` co poprawiłeś, w \`findings_skipped\` czego świadomie nie poprawiłeś i dlaczego, \`deviations\` pusta lista."
	[[ $status == done ]] || fail "code review nie zwrócił done: $reason (katalog: $run)"
	check_clean
	fetch_head "$base"
	if [[ -n $(git rev-list "$base..$head") ]]; then
		land "$base" "code review: $2"
	fi
	review_report=$(report "Code review")
}

do_single() {
	local n=$1 title impl_report labels
	human=
	title=$(gh issue view "$n" --json title --jq .title)
	if [[ -n $resume ]] && git rev-parse -q --verify "refs/remotes/origin/$branch" >/dev/null; then
		# implementacja już wylądowała na remote, klon to klon review
		[[ -z $(pr_number) ]] || fail "PR dla $branch już istnieje — sprawdź go i zdejmij ready-for-agent z #$n ręcznie"
		impl_report=$(printf '### Implementacja\n\nPraca wznowiona po przerwaniu; raport przerwanej sesji: `.ralph/_logs/ticket-%s.json`.\n' "$n")
	else
		get_clone "$start" "$impl_model" "$impl_effort"
		write_context "$n"
		run_claude "$R/_logs/ticket-$n.json" "/mattpocock-skills:implement Zrealizuj ten ticket (JSON z GitHuba: tytuł, body, komentarze):

$(ticket_json "$n")

$(rules)"

		case $status in
		needs_info)
			quote gh issue comment "$n" --body "ralph: nie podjąłem się ticketu. $reason"
			quote gh issue edit "$n" --remove-label ready-for-agent --add-label needs-info
			cleanup
			return
			;;
		done) ;;
		*) fail "brak statusu w wyniku (katalog: $run)" ;;
		esac

		land "$base" "$title (#$n)"
		impl_report=$(report "Implementacja")
	fi
	review "$n" "$title" single
	pr_labels
	quote gh pr create --base main --head "$branch" --title "$title" "${labels[@]}" \
		--body "$(printf 'Closes #%s\n\n%s\n' "$n" "$(git log --format='- %s' origin/main.."$base")")"
	pr=$(pr_number)
	pr_comment "$impl_report"
	pr_comment "$review_report"
	quote gh issue edit "$n" --remove-label ready-for-agent
	cleanup
}

do_spec() {
	local n=$1 spec c children title closes ctitle labels
	human=
	pr=$(pr_number)
	spec=$(ticket_json "$n")
	title=$(jq -r .title <<<"$spec")

	while true; do
		children=$(open_children "$n")
		[[ $children != "[]" ]] || break
		c=
		for c in $(jq '.[].number' <<<"$children"); do
			if ! blocked "$(issue "$c")"; then
				break
			fi
			c=
		done
		[[ -n $c ]] || fail "spec #$n: zostały otwarte dzieci, ale wszystkie są zablokowane (katalog: $run)"

		ctitle=$(gh issue view "$c" --json title --jq .title)
		announce "spec #$n: dziecko #$c $ctitle"
		get_clone "$start" "$impl_model" "$impl_effort"
		write_context "$c" "$n"
		run_claude "$R/_logs/ticket-$c.json" "/mattpocock-skills:implement Zrealizuj ten ticket (JSON z GitHuba: tytuł, body, komentarze):

$(ticket_json "$c")

Jest częścią tego speca (kontekst; poprzednie tickety speca są już w historii brancha):

$spec

$(rules)"

		case $status in
		needs_info)
			quote gh issue comment "$c" --body "ralph: nie podjąłem się ticketu. $reason"
			quote gh issue edit "$c" --remove-label ready-for-agent --add-label needs-info
			quote gh issue comment "$n" --body "ralph: stop na #$c (needs-info). Zrobione dzieci są na branchu $branch."
			quote gh issue edit "$n" --remove-label ready-for-agent --add-label needs-info
			fail "spec #$n: dziecko #$c wymaga doprecyzowania (katalog: $run)"
			;;
		done) ;;
		*) fail "brak statusu w wyniku (katalog: $run)" ;;
		esac

		land "$base" "$ctitle (#$c)"
		start=$branch
		if [[ -z $pr ]]; then
			closes=$(gh api "repos/{owner}/{repo}/issues/$n/sub_issues?per_page=100" --jq '.[].number' | sed 's/^/Closes #/')
			quote gh pr create --draft --base main --head "$branch" --title "$title" \
				--body "$(printf 'Closes #%s\n%s\n' "$n" "$closes")"
			pr=$(pr_number)
		fi
		pr_comment "$(report "#$c $ctitle")"
		quote gh issue close "$c" --comment "ralph: zrobione w $branch @ $(git rev-parse --short "$base"), raport w PR #$pr"
		quote gh issue edit "$c" --remove-label ready-for-agent
	done

	review "$n" "$title" spec

	closes=$(gh api "repos/{owner}/{repo}/issues/$n/sub_issues?per_page=100" --jq '.[].number' | sed 's/^/Closes #/')
	pr_comment "$review_report"
	# REST zamiast `gh pr edit`: ten ciągnie projectCards (Projects classic, wycofane) i pada, zob. cli/cli#14598.
	quote gh api -X PATCH "repos/{owner}/{repo}/pulls/$pr" \
		-f body="$(printf 'Closes #%s\n%s\n\n%s\n' "$n" "$closes" "$(git log --format='- %s' origin/main.."$base")")"
	pr_labels
	if ((${#labels[@]})); then
		quote gh api "repos/{owner}/{repo}/issues/$pr/labels" -f 'labels[]=ready-for-human'
	fi
	quote gh pr ready "$pr"
	quote gh issue edit "$n" --remove-label ready-for-agent
	cleanup
}

# preflight: wszystko, co da się sprawdzić bez tokenów, zanim ruszy pierwsza sesja claude.
preflight() {
	local c f v url out line log=$R/_logs/preflight-gate.txt
	say "preflight"
	for c in bwrap gh jq git go claude; do
		command -v "$c" >/dev/null || fail "preflight: brak $c w PATH"
	done
	for f in "$HOME/.local/share/claude" "$HOME/.claude/plugins" "$HOME/.claude/settings.json" \
		"$HOME/go/bin/golangci-lint" "$HOME/go/bin/gopls" /usr/local/go /opt; do
		[[ -e $f ]] || fail "preflight: brak $f (sandbox go bind-mountuje)"
	done
	# Pakiet Debiana (2.46) jest za stary; instalacja z repo GitHuba: README.
	v=$(gh --version | sed -n '1s/^gh version \([0-9.]*\).*/\1/p')
	[[ $(printf '%s\n' "$gh_min_version" "$v" | sort -V | head -n 1) == "$gh_min_version" ]] ||
		fail "preflight: gh $v jest za stary (min $gh_min_version), instalacja: README"
	gh auth status >/dev/null 2>&1 || fail "preflight: gh nie jest zalogowany (gh auth login)"
	[[ -f $root/.env ]] || fail "preflight: brak $root/.env — testy z bazą potrzebują DATABASE_TEST_URL (README, task test:setup)"
	url=$(sed -n 's/^DATABASE_TEST_URL=//p' "$root/.env" | tail -n 1)
	url=${url//[\"\']/}
	[[ -n $url ]] || fail "preflight: brak DATABASE_TEST_URL w $root/.env"
	if command -v psql >/dev/null; then
		out=$(psql "$url" -Atc 'select rolcreatedb from pg_roles where rolname = current_user' 2>&1) ||
			fail "preflight: DATABASE_TEST_URL nie działa: $out"
		[[ $out == t ]] || fail "preflight: rola z DATABASE_TEST_URL nie ma CREATEDB (task test:setup)"
	fi

	# Ta sama bramka co przy landzie, na czystym main: gdy pada tu, to nie wina agenta.
	say "preflight: bramka na czystym main"
	branch=main run=$R/_preflight
	fresh_clone main
	if ! out=$(sandbox "$run" bash -c "$gate_cmd" 2>&1); then
		printf '%s\n' "$out" >"$log"
		tail -n 40 "$log" | while IFS= read -r line; do say "$line"; done
		rm -rf "$run"
		run=
		fail "preflight: bramka pada na czystym main (log: $log)"
	fi
	rm -rf "$run"
	run=
}

# process N KIND: jeden ticket (single|spec); przy resume=1 w istniejącym klonie $R/ticket-N.
process() {
	local n=$1 kind=$2
	branch=ticket-$n
	run=$R/$branch
	quote git fetch -q --prune origin
	start=main
	if git rev-parse -q --verify "refs/remotes/origin/$branch" >/dev/null; then
		if [[ $kind == spec ]]; then
			[[ -z $(gh pr list --head "$branch" --state all --json number,state,isDraft --jq '.[] | select(.state != "OPEN" or .isDraft == false)') ]] ||
				fail "PR dla $branch już istnieje (albo istniał) i nie jest draftem — sprawdź go i zdejmij ready-for-agent z #$n ręcznie"
			announce "wznawiam spec #$n z origin/$branch"
			start=$branch
		else
			[[ -n $resume ]] || fail "branch $branch istnieje na remote, dokończ PR albo usuń branch (git push origin --delete $branch)"
		fi
	fi
	"do_$kind" "$n"
	handled+=("$n")
}

main() {
	local max=${1:-10} i picked n kind d leftovers json line
	handled=()
	local token_file t
	cd "$(git rev-parse --show-toplevel)"
	root=$(pwd -P)
	R=$root/.ralph
	cache_dir=$R/_cache
	token_file=$R/token
	mkdir -p "$cache_dir"/{gomod,gobuild,npm} "$R/_logs"

	t=
	if [[ -f $token_file ]]; then
		t=$(<"$token_file")
	fi
	t=${t//[[:space:]]/}
	if [[ -z $t ]]; then
		[[ -t 0 && -t 1 ]] || fail "brak tokenu Claude w $token_file. Uruchom ralph interaktywnie (w terminalu) albo zapisz token ręcznie do .ralph/token (z 'claude setup-token')."
		claude setup-token
		# Terminal zawija długi token — przy kopiowaniu bywa rozbity na kilka linii, więc sklejamy do pustej linii.
		say "wklej token z powyższego wyjścia (może być w kilku liniach), zakończ pustą linią:"
		t=
		while IFS= read -rs line && [[ -n $line ]]; do
			t+=$line
		done
		echo
		t=${t//[[:space:]]/}
		[[ $t == sk-ant-* ]] || fail "to nie wygląda na token z 'claude setup-token' (oczekiwany prefiks sk-ant-)"
		(umask 077; printf '%s\n' "$t" >"$token_file")
	fi
	token=$t

	claude_bin=$(readlink -f "$(command -v claude)")

	# Klony po twardej porażce: zamknięty ticket → sprzątamy, ready-for-agent → wznawiamy, zanim ruszy kolejka.
	leftovers=("$R"/ticket-*)
	if [[ ! -e ${leftovers[0]} ]]; then
		leftovers=()
	fi
	if ((${#leftovers[@]} == 0)) && [[ -z $(gh issue list --state open --label ready-for-agent --limit 1 --json number --jq '.[]') ]]; then
		say "done"
		exit 0
	fi
	preflight

	if ((${#leftovers[@]})); then
		quote git fetch -q --prune origin
	fi
	for d in "${leftovers[@]}"; do
		n=${d##*/ticket-}
		json=$(issue "$n")
		branch=ticket-$n
		if [[ $(jq -r .state <<<"$json") == closed ]]; then
			say "#$n zamknięty — sprzątam $d"
			rm -rf "$d"
			if git rev-parse -q --verify "refs/remotes/origin/$branch" >/dev/null && [[ -z $(pr_number) ]]; then
				quote git push -q origin --delete "$branch"
			fi
		elif jq -e 'any(.labels[]; .name == "ready-for-agent")' <<<"$json" >/dev/null; then
			kind=single
			if (($(jq '.sub_issues_summary.total // 0' <<<"$json") > 0)); then
				kind=spec
			fi
			announce "wznawiam #$n $(jq -r .title <<<"$json") ($kind)"
			resume=1
			process "$n" "$kind"
			resume=
		else
			say "pomijam $d — #$n nie ma ready-for-agent (wróci, gdy label wróci)"
		fi
	done

	for ((i = 1; i <= max; i++)); do
		if [[ -z $(gh issue list --state open --label ready-for-agent --limit 1 --json number --jq '.[]') ]]; then
			say "done"
			exit 0
		fi
		picked=$(pick)
		read -r n kind <<<"$picked"
		if [[ -z ${n:-} ]]; then
			say "brak ticketu do wzięcia — pozostałe są zablokowane albo pominięte"
			exit 0
		fi

		announce "ralph $i/$max: #$n $(gh issue view "$n" --json title --jq .title) ($kind)"
		process "$n" "$kind"
	done

	say "osiągnięto limit $max iteracji"
	exit 1
}

# Wczytanie przez `source` (np. test sandboxa) nie uruchamia pętli.
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	main "$@"
fi
